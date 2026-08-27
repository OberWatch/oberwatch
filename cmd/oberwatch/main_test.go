package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OberWatch/oberwatch/internal/alert"
	"github.com/OberWatch/oberwatch/internal/config"
	"github.com/OberWatch/oberwatch/internal/storage"
)

func TestNewRootCmd_RegistersCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		command  string
		name     string
		wantName string
	}{
		{name: "serve", command: "serve", wantName: "serve"},
		{name: "gate", command: "gate", wantName: "gate"},
		{name: "trace", command: "trace", wantName: "trace"},
		{name: "test", command: "test", wantName: "test"},
		{name: "test run", command: "test run", wantName: "run"},
		{name: "validate", command: "validate", wantName: "validate"},
		{name: "init", command: "init", wantName: "init"},
		{name: "version", command: "version", wantName: "version"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := newRootCmd()
			parts := strings.Fields(tt.command)
			cmd, _, err := root.Find(parts)
			if err != nil {
				t.Fatalf("Find(%v) error = %v", parts, err)
			}
			if cmd == nil {
				t.Fatalf("Find(%v) returned nil command", parts)
			}
			if cmd.Name() != tt.wantName {
				t.Fatalf("Find(%v) command = %q, want %q", parts, cmd.Name(), tt.wantName)
			}
		})
	}
}

func TestNewRootCmd_DefinesExpectedFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		command string
		flags   string
		name    string
	}{
		{
			name:    "root global flags",
			command: "",
			flags:   "config,log-level,log-format,version",
		},
		{
			name:    "serve flags",
			command: "serve",
			flags:   "port,host,admin-token,no-dashboard,no-trace,no-gate",
		},
		{
			name:    "gate flags",
			command: "gate",
			flags:   "port,host,admin-token",
		},
		{
			name:    "trace flags",
			command: "trace",
			flags:   "port,storage,db-path,retention",
		},
		{
			name:    "test run flags",
			command: "test run",
			flags:   "concurrency,timeout,output,output-file,fail-fast,filter,proxy-url,judge-model,judge-key,dry-run",
		},
		{
			name:    "validate inherited config flag",
			command: "validate",
			flags:   "config",
		},
		{
			name:    "init flags",
			command: "init",
			flags:   "output,force",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := newRootCmd()
			cmd := root
			if tt.command != "" {
				parts := strings.Fields(tt.command)
				found, _, err := root.Find(parts)
				if err != nil {
					t.Fatalf("Find(%v) error = %v", parts, err)
				}
				cmd = found
			}

			for _, name := range splitCSV(tt.flags) {
				flag := cmd.Flags().Lookup(name)
				if flag == nil {
					flag = cmd.PersistentFlags().Lookup(name)
				}
				if flag == nil {
					flag = cmd.InheritedFlags().Lookup(name)
				}
				if flag == nil {
					t.Fatalf("command %q missing flag %q", cmd.CommandPath(), name)
				}
			}
		})
	}
}

func TestServeAndGate_BannerReflectsFlags(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		wantContains []string
	}{
		{
			name: "serve defaults from config",
			args: []string{"serve"},
			wantContains: []string{
				"Proxy:     http://0.0.0.0:8080",
				"Dashboard: http://0.0.0.0:8080",
				"Gate:      enabled",
				"Trace:     enabled",
			},
		},
		{
			name: "serve override flags",
			args: []string{"serve", "--host", "127.0.0.1", "--port", "9090", "--no-dashboard", "--no-trace", "--no-gate"},
			wantContains: []string{
				"Proxy:     http://127.0.0.1:9090",
				"Dashboard: disabled",
				"Gate:      disabled",
				"Trace:     disabled",
			},
		},
		{
			name: "gate command banner",
			args: []string{"gate", "--host", "127.0.0.1", "--port", "9091"},
			wantContains: []string{
				"Proxy:     http://127.0.0.1:9091",
				"Dashboard: disabled",
				"Gate:      enabled",
				"Trace:     disabled",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OW_TEST_MODE", "1")
			cfgPath := writeValidConfig(t)

			root := newRootCmd()
			var stdout bytes.Buffer
			root.SetOut(&stdout)
			root.SetErr(&bytes.Buffer{})
			root.SetArgs(append([]string{"--config", cfgPath}, tt.args...))

			if err := root.Execute(); err != nil {
				t.Fatalf("Execute() error = %v", err)
			}

			out := stdout.String()
			if !strings.Contains(out, fmt.Sprintf("Oberwatch %s", version)) {
				t.Fatalf("stdout = %q, want version header", out)
			}
			if !strings.Contains(out, "Config:    "+cfgPath) {
				t.Fatalf("stdout = %q, want config path %q", out, cfgPath)
			}
			for _, want := range tt.wantContains {
				if !strings.Contains(out, want) {
					t.Fatalf("stdout = %q, want substring %q", out, want)
				}
			}
		})
	}
}

func TestServe_UsesDefaultsWhenNoConfigFileExists(t *testing.T) {
	t.Setenv("OW_TEST_MODE", "1")

	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	workDir := t.TempDir()
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(origWD); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	root := newRootCmd()
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"serve"})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	out := stdout.String()
	for _, want := range []string{
		"Proxy:     http://0.0.0.0:8080",
		"Dashboard: http://0.0.0.0:8080",
		"Config:    (defaults/env only)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout = %q, want substring %q", out, want)
		}
	}
}

func TestTraceAndTestRun_FlagParsing(t *testing.T) {
	tests := []struct {
		args       string
		name       string
		wantErrSub string
	}{
		{
			name: "trace valid flags",
			args: "trace --port 8082 --storage memory --retention 24h",
		},
		{
			name:       "trace invalid storage",
			args:       "trace --storage bad-storage",
			wantErrSub: "--storage must be one of memory, sqlite",
		},
		{
			name: "test run valid flags",
			args: "test run --concurrency 8 --timeout 45s --output json --filter invoice scenarios/",
		},
		{
			name:       "test run invalid output",
			args:       "test run --output xml",
			wantErrSub: "--output must be one of console, junit, json",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			cfgPath := writeValidConfig(t)

			root := newRootCmd()
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})
			args := append([]string{"--config", cfgPath}, strings.Fields(tt.args)...)
			root.SetArgs(args)

			err := root.Execute()
			if tt.wantErrSub == "" {
				if err != nil {
					t.Fatalf("Execute() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) {
				t.Fatalf("Execute() error = %v, want substring %q", err, tt.wantErrSub)
			}
		})
	}
}

func TestPathIsMountPointAndEmergencyStopSetting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		mountInfo         string
		settingValue      string
		wantMounted       bool
		wantEmergencyStop bool
	}{
		{
			name:              "mounted data path and true emergency flag",
			mountInfo:         "36 25 0:32 / /data rw - overlay overlay rw\n",
			settingValue:      "true",
			wantMounted:       true,
			wantEmergencyStop: true,
		},
		{
			name:              "missing mount and false emergency flag",
			mountInfo:         "36 25 0:32 / /tmp rw - overlay overlay rw\n",
			settingValue:      "false",
			wantMounted:       false,
			wantEmergencyStop: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mountInfoPath := filepath.Join(t.TempDir(), "mountinfo")
			if err := os.WriteFile(mountInfoPath, []byte(tt.mountInfo), 0o644); err != nil {
				t.Fatalf("WriteFile(mountinfo) error = %v", err)
			}

			mounted, err := pathIsMountPoint(mountInfoPath, "/data")
			if err != nil {
				t.Fatalf("pathIsMountPoint() error = %v", err)
			}
			if mounted != tt.wantMounted {
				t.Fatalf("pathIsMountPoint() = %v, want %v", mounted, tt.wantMounted)
			}

			store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "settings.db"), 0, nil)
			if err != nil {
				t.Fatalf("NewSQLiteStore() error = %v", err)
			}
			t.Cleanup(func() {
				_ = store.Close()
			})
			if setErr := store.SetSetting(context.Background(), "emergency_stop", tt.settingValue); setErr != nil {
				t.Fatalf("SetSetting(emergency_stop) error = %v", setErr)
			}

			enabled, err := loadEmergencyStopSetting(context.Background(), store)
			if err != nil {
				t.Fatalf("loadEmergencyStopSetting() error = %v", err)
			}
			if enabled != tt.wantEmergencyStop {
				t.Fatalf("loadEmergencyStopSetting() = %v, want %v", enabled, tt.wantEmergencyStop)
			}
		})
	}
}

func TestWarnIfContainerDataDirNotMounted_LogsWarning(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dockerEnvPath := filepath.Join(dir, ".dockerenv")
	mountInfoPath := filepath.Join(dir, "mountinfo")

	if err := os.WriteFile(dockerEnvPath, []byte("container"), 0o644); err != nil {
		t.Fatalf("WriteFile(dockerenv) error = %v", err)
	}
	if err := os.WriteFile(mountInfoPath, []byte("36 25 0:32 / /tmp rw - overlay overlay rw\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(mountinfo) error = %v", err)
	}

	var logOutput bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logOutput, nil))
	warnIfContainerDataDirNotMounted(logger, dockerEnvPath, mountInfoPath, "/data")

	if !strings.Contains(logOutput.String(), "data directory is not a mounted volume") {
		t.Fatalf("log output = %q, want mount warning", logOutput.String())
	}
}

func TestAlertDispatchFunc_Dispatches(t *testing.T) {
	t.Parallel()

	var called bool
	dispatcher := alertDispatchFunc(func(_ context.Context, _ alert.Alert) {
		called = true
	})
	dispatcher.Dispatch(context.Background(), alert.Alert{})
	if !called {
		t.Fatal("Dispatch() did not invoke wrapped function")
	}
}

func TestMainHelpers(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep helper test case fields ordered for readability.
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{
			name: "validate helpers cover success and failure paths",
			run: func(t *testing.T) {
				t.Helper()
				if err := validateGlobalFlags("info", "text"); err != nil {
					t.Fatalf("validateGlobalFlags(valid) error = %v", err)
				}
				if err := validateGlobalFlags("bad", "text"); err == nil {
					t.Fatal("validateGlobalFlags(invalid level) error = nil, want non-nil")
				}
				if err := validateGlobalFlags("info", "bad"); err == nil {
					t.Fatal("validateGlobalFlags(invalid format) error = nil, want non-nil")
				}
				if err := validatePort(8080, "port"); err != nil {
					t.Fatalf("validatePort(valid) error = %v", err)
				}
				if err := validatePort(70000, "port"); err == nil {
					t.Fatal("validatePort(invalid) error = nil, want non-nil")
				}
				if _, err := parsePositiveDuration("15s", "timeout"); err != nil {
					t.Fatalf("parsePositiveDuration(valid) error = %v", err)
				}
				if _, err := parsePositiveDuration("0s", "timeout"); err == nil {
					t.Fatal("parsePositiveDuration(zero) error = nil, want non-nil")
				}
			},
		},
		{
			name: "file helpers cover missing path branch",
			run: func(t *testing.T) {
				t.Helper()
				existingPath := filepath.Join(t.TempDir(), "exists")
				if err := os.WriteFile(existingPath, []byte("x"), 0o644); err != nil {
					t.Fatalf("WriteFile(existing) error = %v", err)
				}
				exists, err := fileExists(existingPath)
				if err != nil {
					t.Fatalf("fileExists(existing) error = %v", err)
				}
				if !exists {
					t.Fatal("fileExists(existing) = false, want true")
				}

				missingPath := filepath.Join(t.TempDir(), "missing")
				exists, err = fileExists(missingPath)
				if err != nil {
					t.Fatalf("fileExists(missing) error = %v", err)
				}
				if exists {
					t.Fatal("fileExists(missing) = true, want false")
				}
			},
		},
		{
			name: "logger and runtime config branches execute",
			run: func(t *testing.T) {
				t.Helper()
				logger := newLogger(config.LogLevelDebug, config.LogFormatJSON, &bytes.Buffer{})
				logger.Debug("debug message")
				textLogger := newLogger(config.LogLevelWarn, config.LogFormatText, &bytes.Buffer{})
				textLogger.Warn("warn message")

				cfgPath := writeValidConfig(t)
				rootOpts := &rootOptions{configPath: cfgPath, logLevel: "debug", logFormat: "json"}
				cmd := newRootCmd()
				cmd.SetArgs([]string{"serve", "--log-level", "debug", "--log-format", "json"})
				serveCmd, _, err := cmd.Find([]string{"serve"})
				if err != nil {
					t.Fatalf("Find(serve) error = %v", err)
				}
				if parseErr := serveCmd.ParseFlags([]string{"--log-level", "debug", "--log-format", "json"}); parseErr != nil {
					t.Fatalf("ParseFlags() error = %v", parseErr)
				}
				cfg, _, err := loadRuntimeConfig(serveCmd, rootOpts)
				if err != nil {
					t.Fatalf("loadRuntimeConfig() error = %v", err)
				}
				if cfg.Server.LogLevel != config.LogLevelDebug || cfg.Server.LogFormat != config.LogFormatJSON {
					t.Fatalf("loadRuntimeConfig() log overrides = %q/%q, want debug/json", cfg.Server.LogLevel, cfg.Server.LogFormat)
				}

				root := newRootCmd()
				var stdout bytes.Buffer
				root.SetOut(&stdout)
				root.SetErr(&bytes.Buffer{})
				root.SetArgs([]string{"test"})
				if err := root.Execute(); err != nil {
					t.Fatalf("root.Execute(test) error = %v", err)
				}
				if !strings.Contains(stdout.String(), "Run behavioral test scenarios") {
					t.Fatalf("test help output = %q, want command help", stdout.String())
				}
			},
		},
		{
			name: "warning and emergency setting helpers cover quiet branches",
			run: func(t *testing.T) {
				t.Helper()
				store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "settings.db"), 0, nil)
				if err != nil {
					t.Fatalf("NewSQLiteStore() error = %v", err)
				}
				t.Cleanup(func() {
					_ = store.Close()
				})

				enabled, err := loadEmergencyStopSetting(context.Background(), store)
				if err != nil {
					t.Fatalf("loadEmergencyStopSetting(missing) error = %v", err)
				}
				if enabled {
					t.Fatal("loadEmergencyStopSetting(missing) = true, want false")
				}

				var logOutput bytes.Buffer
				logger := slog.New(slog.NewTextHandler(&logOutput, nil))
				warnIfContainerDataDirNotMounted(logger, filepath.Join(t.TempDir(), "missing-dockerenv"), filepath.Join(t.TempDir(), "missing-mountinfo"), "/data")
				if logOutput.Len() != 0 {
					t.Fatalf("warnIfContainerDataDirNotMounted() log = %q, want empty output", logOutput.String())
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.run(t)
		})
	}
}

func TestVersionCommandAndGlobalVersionFlag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		args        []string
		notContains []string
	}{
		{name: "root global version", args: []string{"--version"}},
		{name: "serve with global version", args: []string{"serve", "--version"}, notContains: []string{"Proxy:", "Dashboard:"}},
		{name: "version subcommand", args: []string{"version"}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := newRootCmd()
			var stdout bytes.Buffer
			root.SetOut(&stdout)
			root.SetErr(&bytes.Buffer{})
			root.SetArgs(tt.args)

			if err := root.Execute(); err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			out := stdout.String()
			want := "oberwatch " + displayVersion()
			if !strings.Contains(out, want) {
				t.Fatalf("stdout = %q, want substring %q", out, want)
			}
			for _, notWant := range tt.notContains {
				if strings.Contains(out, notWant) {
					t.Fatalf("stdout = %q, should not contain %q", out, notWant)
				}
			}
		})
	}
}

func TestSourceBuildCLIProductVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "global version flag", args: []string{"--version"}},
		{name: "version command", args: []string{"version"}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := newRootCmd()
			var stdout bytes.Buffer
			root.SetOut(&stdout)
			root.SetErr(&bytes.Buffer{})
			root.SetArgs(tt.args)

			if err := root.Execute(); err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if got, want := strings.SplitN(stdout.String(), "\n", 2)[0], "oberwatch v0.1.2"; got != want {
				t.Fatalf("version line = %q, want %q", got, want)
			}
		})
	}
}

func TestDisplayVersion_IgnoresChannel(t *testing.T) {
	originalVersion := version
	originalChannel := channel
	t.Cleanup(func() {
		version = originalVersion
		channel = originalChannel
	})

	version = "v1.2.3"
	channel = "beta"

	if got := displayVersion(); got != "v1.2.3" {
		t.Fatalf("displayVersion() = %q, want %q", got, "v1.2.3")
	}
}

func TestDisplayVersion_DefaultsWhenVersionEmpty(t *testing.T) {
	originalVersion := version
	originalChannel := channel
	t.Cleanup(func() {
		version = originalVersion
		channel = originalChannel
	})

	version = "   "
	channel = "staging"

	if got := displayVersion(); got != "v0.0.0" {
		t.Fatalf("displayVersion() = %q, want %q", got, "v0.0.0")
	}
}

func TestValidateAndInitCommands(t *testing.T) {
	// Each case gets a fresh temp dir; {dir} in args and expectations is
	// replaced with that directory so paths can be asserted exactly.
	//nolint:govet // Keep table fields grouped for clearer command test setup.
	tests := []struct {
		name        string
		args        []string
		precreate   map[string]string
		wantErrSubs []string
		wantStdout  string
		wantStderr  string
		wantFile    string
		wantContent string
	}{
		{
			name:       "validate succeeds with valid config via root flag",
			args:       []string{"--config", "{dir}/oberwatch.toml", "validate"},
			precreate:  map[string]string{"oberwatch.toml": config.StarterTOML},
			wantStdout: "config {dir}/oberwatch.toml is valid\n",
		},
		{
			name:       "validate succeeds with config flag after subcommand",
			args:       []string{"validate", "--config", "{dir}/custom.toml"},
			precreate:  map[string]string{"custom.toml": config.StarterTOML},
			wantStdout: "config {dir}/custom.toml is valid\n",
		},
		{
			name:        "validate fails with missing config",
			args:        []string{"--config", "{dir}/missing.toml", "validate"},
			wantErrSubs: []string{"not found", "missing.toml", "--config flag"},
		},
		{
			name:        "validate fails with malformed config",
			args:        []string{"validate", "--config", "{dir}/bad.toml"},
			precreate:   map[string]string{"bad.toml": "[server]\nport = \n"},
			wantErrSubs: []string{"parse config", "bad.toml", "line 2"},
		},
		{
			name:        "validate fails with semantically invalid config",
			args:        []string{"validate", "--config", "{dir}/bad.toml"},
			precreate:   map[string]string{"bad.toml": "[server]\nport = 0\nhost = \"\"\n"},
			wantErrSubs: []string{"validate config", "bad.toml", "server.port", "server.host"},
		},
		{
			name:       "validate warns about legacy admin token on stderr",
			args:       []string{"validate", "--config", "{dir}/legacy.toml"},
			precreate:  map[string]string{"legacy.toml": "[server]\nadmin_token = \"legacy\"\n"},
			wantStdout: "config {dir}/legacy.toml is valid\n",
			wantStderr: "warning: server.admin_token is set but not used",
		},
		{
			name:        "init writes starter file to nested output path",
			args:        []string{"init", "--output", "{dir}/nested/oberwatch.toml"},
			wantStdout:  "wrote starter config to {dir}/nested/oberwatch.toml\nnext: oberwatch serve --config {dir}/nested/oberwatch.toml\n",
			wantFile:    "nested/oberwatch.toml",
			wantContent: config.StarterTOML,
		},
		{
			name:        "init fails when file exists without force",
			args:        []string{"init", "--output", "{dir}/oberwatch.toml"},
			precreate:   map[string]string{"oberwatch.toml": "old"},
			wantErrSubs: []string{"refusing to overwrite existing file", "--force"},
			wantFile:    "oberwatch.toml",
			wantContent: "old",
		},
		{
			name:        "init force overwrites existing file",
			args:        []string{"init", "--force", "--output", "{dir}/oberwatch.toml"},
			precreate:   map[string]string{"oberwatch.toml": "old"},
			wantStdout:  "wrote starter config to {dir}/oberwatch.toml\nnext: oberwatch serve --config {dir}/oberwatch.toml\n",
			wantFile:    "oberwatch.toml",
			wantContent: config.StarterTOML,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			expand := func(s string) string { return strings.ReplaceAll(s, "{dir}", dir) }

			for name, content := range tt.precreate {
				path := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("MkdirAll() error = %v", err)
				}
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatalf("WriteFile(%s) error = %v", name, err)
				}
			}

			args := make([]string, 0, len(tt.args))
			for _, arg := range tt.args {
				args = append(args, expand(arg))
			}

			root := newRootCmd()
			var stdout, stderr bytes.Buffer
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			root.SetArgs(args)

			err := root.Execute()
			if len(tt.wantErrSubs) > 0 {
				if err == nil {
					t.Fatalf("Execute() error = nil, want error containing %v", tt.wantErrSubs)
				}
				for _, sub := range tt.wantErrSubs {
					if !strings.Contains(err.Error(), sub) {
						t.Fatalf("Execute() error = %q, want substring %q", err.Error(), sub)
					}
				}
				if stdout.Len() != 0 {
					t.Fatalf("stdout = %q, want empty on failure", stdout.String())
				}
			} else {
				if err != nil {
					t.Fatalf("Execute() error = %v", err)
				}
				if got, want := stdout.String(), expand(tt.wantStdout); got != want {
					t.Fatalf("stdout = %q, want %q", got, want)
				}
			}
			if tt.wantStderr != "" && !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Fatalf("stderr = %q, want substring %q", stderr.String(), tt.wantStderr)
			}
			if tt.wantStderr == "" && len(tt.wantErrSubs) == 0 && stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}

			if tt.wantFile != "" {
				data, readErr := os.ReadFile(filepath.Join(dir, tt.wantFile))
				if readErr != nil {
					t.Fatalf("ReadFile(%s) error = %v", tt.wantFile, readErr)
				}
				if string(data) != tt.wantContent {
					t.Fatalf("%s content mismatch: got %d bytes, want %d bytes", tt.wantFile, len(data), len(tt.wantContent))
				}
			}
		})
	}
}

func TestInitDefaultOutputRoundTripsThroughValidate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(origWD); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	initRoot := newRootCmd()
	var initOut bytes.Buffer
	initRoot.SetOut(&initOut)
	initRoot.SetErr(&bytes.Buffer{})
	initRoot.SetArgs([]string{"init"})
	if err := initRoot.Execute(); err != nil {
		t.Fatalf("init Execute() error = %v", err)
	}
	if got, want := initOut.String(), config.InitSuccessMessage(config.DefaultInitOutput); got != want {
		t.Fatalf("init stdout = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "oberwatch.toml")); err != nil {
		t.Fatalf("init did not write ./oberwatch.toml: %v", err)
	}

	validateRoot := newRootCmd()
	var validateOut, validateErr bytes.Buffer
	validateRoot.SetOut(&validateOut)
	validateRoot.SetErr(&validateErr)
	validateRoot.SetArgs([]string{"validate"})
	if err := validateRoot.Execute(); err != nil {
		t.Fatalf("validate Execute() error = %v", err)
	}
	if got, want := validateOut.String(), "config ./oberwatch.toml is valid\n"; got != want {
		t.Fatalf("validate stdout = %q, want %q", got, want)
	}
	if validateErr.Len() != 0 {
		t.Fatalf("validate stderr = %q, want no warnings for the starter config", validateErr.String())
	}
}

func TestRunAndWriteStarterConfig(t *testing.T) {
	tests := []struct {
		name       string
		runTest    func(*testing.T)
		wantErrSub string
	}{
		{
			name: "run executes version command",
			runTest: func(t *testing.T) {
				t.Helper()
				originalArgs := os.Args
				defer func() { os.Args = originalArgs }()
				os.Args = []string{"oberwatch", "version"}

				if err := run(); err != nil {
					t.Fatalf("run() error = %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			tt.runTest(t)
		})
	}
}

func TestMainEntryPoint_VersionCommand(t *testing.T) {
	originalArgs := os.Args
	originalStdout := os.Stdout
	originalStderr := os.Stderr
	t.Cleanup(func() {
		os.Args = originalArgs
		os.Stdout = originalStdout
		os.Stderr = originalStderr
	})

	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(stdout) error = %v", err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(stderr) error = %v", err)
	}
	t.Cleanup(func() {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		_ = stderrReader.Close()
		_ = stderrWriter.Close()
	})

	os.Stdout = stdoutWriter
	os.Stderr = stderrWriter
	os.Args = []string{"oberwatch", "version"}

	main()

	if closeErr := stdoutWriter.Close(); closeErr != nil {
		t.Fatalf("stdoutWriter.Close() error = %v", closeErr)
	}
	if closeErr := stderrWriter.Close(); closeErr != nil {
		t.Fatalf("stderrWriter.Close() error = %v", closeErr)
	}

	stdoutBytes, err := io.ReadAll(stdoutReader)
	if err != nil {
		t.Fatalf("ReadAll(stdout) error = %v", err)
	}
	stderrBytes, err := io.ReadAll(stderrReader)
	if err != nil {
		t.Fatalf("ReadAll(stderr) error = %v", err)
	}

	if !strings.Contains(string(stdoutBytes), "oberwatch "+displayVersion()) {
		t.Fatalf("stdout = %q, want version output", string(stdoutBytes))
	}
	if len(stderrBytes) != 0 {
		t.Fatalf("stderr = %q, want empty stderr", string(stderrBytes))
	}
}

func TestMainEntryPoint_ExitOnError(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestMainEntryPoint_ErrorHelper")
	cmd.Env = append(os.Environ(), "OW_MAIN_ERROR_HELPER=1")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("CombinedOutput() error = nil, want non-nil exit")
	}

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("error type = %T, want *exec.ExitError", err)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("exit code = %d, want 1", exitErr.ExitCode())
	}
	if !strings.Contains(string(output), "error: --log-level must be one of") {
		t.Fatalf("output = %q, want validation error", string(output))
	}
}

func TestMainEntryPoint_ErrorHelper(t *testing.T) {
	if os.Getenv("OW_MAIN_ERROR_HELPER") != "1" {
		return
	}

	os.Args = []string{"oberwatch", "--log-level", "bad-level", "version"}
	main()
}

func TestParsePositiveDuration_InvalidDuration(t *testing.T) {
	t.Parallel()

	if _, err := parsePositiveDuration("not-a-duration", "timeout"); err == nil {
		t.Fatal("parsePositiveDuration(invalid) error = nil, want non-nil")
	}
}

func writeValidConfig(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "oberwatch.toml")
	if err := os.WriteFile(path, []byte(config.StarterTOML), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}
