package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OberWatch/oberwatch/internal/config"
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

func TestDisplayVersion_AppendsChannel(t *testing.T) {
	originalVersion := version
	originalChannel := channel
	t.Cleanup(func() {
		version = originalVersion
		channel = originalChannel
	})

	version = "v1.2.3"
	channel = "beta"

	if got := displayVersion(); got != "v1.2.3 (beta)" {
		t.Fatalf("displayVersion() = %q, want %q", got, "v1.2.3 (beta)")
	}
}

func TestValidateAndInitCommands(t *testing.T) {
	//nolint:govet // Keep table fields grouped for clearer command test setup.
	tests := []struct {
		name       string
		args       []string
		wantErrSub string
		wantOutSub string
		checkFile  string
	}{
		{
			name:       "validate succeeds with valid config",
			args:       []string{"validate"},
			wantOutSub: "is valid",
		},
		{
			name:       "validate fails with missing config",
			args:       []string{"validate"},
			wantErrSub: "parse config",
		},
		{
			name:       "init writes starter file",
			args:       []string{"init"},
			wantOutSub: "wrote starter config",
			checkFile:  "exists",
		},
		{
			name:       "init fails when file exists without force",
			args:       []string{"init"},
			wantErrSub: "refusing to overwrite existing file",
			checkFile:  "precreate",
		},
		{
			name:       "init force overwrites existing file",
			args:       []string{"init", "--force"},
			wantOutSub: "wrote starter config",
			checkFile:  "precreate",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			cfgPath := writeValidConfig(t)
			missingPath := filepath.Join(t.TempDir(), "missing.toml")
			outputPath := filepath.Join(t.TempDir(), "nested", "oberwatch.toml")

			if tt.checkFile == "precreate" {
				if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
					t.Fatalf("MkdirAll() error = %v", err)
				}
				if err := os.WriteFile(outputPath, []byte("old"), 0o644); err != nil {
					t.Fatalf("WriteFile(precreate) error = %v", err)
				}
			}

			root := newRootCmd()
			var stdout bytes.Buffer
			root.SetOut(&stdout)
			root.SetErr(&bytes.Buffer{})

			switch tt.name {
			case "validate succeeds with valid config":
				root.SetArgs([]string{"--config", cfgPath, "validate"})
			case "validate fails with missing config":
				root.SetArgs([]string{"--config", missingPath, "validate"})
			default:
				root.SetArgs(append([]string{"init", "--output", outputPath}, tt.args[1:]...))
			}

			err := root.Execute()
			if tt.wantErrSub == "" {
				if err != nil {
					t.Fatalf("Execute() error = %v", err)
				}
				if tt.wantOutSub != "" && !strings.Contains(stdout.String(), tt.wantOutSub) {
					t.Fatalf("stdout = %q, want substring %q", stdout.String(), tt.wantOutSub)
				}
				if tt.checkFile == "exists" || tt.checkFile == "precreate" {
					if _, statErr := os.Stat(outputPath); statErr != nil {
						t.Fatalf("Stat(%q) error = %v", outputPath, statErr)
					}
				}
				return
			}

			if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) {
				t.Fatalf("Execute() error = %v, want substring %q", err, tt.wantErrSub)
			}
		})
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
		{
			name: "writeStarterConfig force creates nested path",
			runTest: func(t *testing.T) {
				t.Helper()
				path := filepath.Join(t.TempDir(), "deep", "oberwatch.toml")
				if err := writeStarterConfig(path, true); err != nil {
					t.Fatalf("writeStarterConfig(force=true) error = %v", err)
				}
				content, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("ReadFile() error = %v", err)
				}
				if !strings.Contains(string(content), "[server]") {
					t.Fatalf("starter config missing [server] section: %q", string(content))
				}
			},
		},
		{
			name: "writeStarterConfig without force errors on existing file",
			runTest: func(t *testing.T) {
				t.Helper()
				path := filepath.Join(t.TempDir(), "oberwatch.toml")
				if err := os.WriteFile(path, []byte("existing"), 0o644); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
				err := writeStarterConfig(path, false)
				if err == nil {
					t.Fatal("writeStarterConfig(force=false) error = nil, want non-nil")
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
