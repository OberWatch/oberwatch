package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultConfig_HasExpectedDefaults(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()

	tests := []struct {
		got  any
		want any
		name string
	}{
		{name: "server.port", got: cfg.Server.Port, want: 8080},
		{name: "server.host", got: cfg.Server.Host, want: "0.0.0.0"},
		{name: "server.dashboard", got: cfg.Server.Dashboard, want: true},
		{name: "server.log_level", got: cfg.Server.LogLevel, want: LogLevelInfo},
		{name: "upstream.default_provider", got: cfg.Upstream.DefaultProvider, want: ProviderOpenAI},
		{name: "upstream.timeout", got: cfg.Upstream.Timeout, want: "120s"},
		{name: "gate.default_budget.period", got: cfg.Gate.DefaultBudget.Period, want: BudgetPeriodDaily},
		{name: "gate.default_budget.action_on_exceed", got: cfg.Gate.DefaultBudget.ActionOnExceed, want: BudgetActionAlert},
		{name: "trace.storage", got: cfg.Trace.Storage, want: TraceStorageSQLite},
		{name: "test.judge.provider", got: cfg.Test.Judge.Provider, want: ProviderAnthropic},
		{name: "alerts.email.smtp_port", got: cfg.Alerts.Email.SMTPPort, want: 587},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if !reflect.DeepEqual(tt.got, tt.want) {
				t.Fatalf("got %v, want %v", tt.got, tt.want)
			}
		})
	}

	if len(cfg.Pricing) != 9 {
		t.Fatalf("DefaultConfig().Pricing length = %d, want 9", len(cfg.Pricing))
	}
}

func TestLoad_AppliesFileDefaultsAndEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "oberwatch.toml")

	content := `
[server]
port = 9090
log_level = "debug"

[upstream]
default_provider = "custom"

[upstream.custom]
base_url = "https://llm.example.com"

[gate]
default_downgrade_chain = ["model-a", "model-b"]
alert_thresholds_pct = [20, 80, 100]

[gate.default_budget]
action_on_exceed = "downgrade"

[alerts.email]
enabled = true
smtp_host = "smtp.example.com"
from = "oberwatch@example.com"
to = ["ops@example.com"]
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Setenv("OBERWATCH_SERVER__PORT", "9191")
	t.Setenv("OBERWATCH_GATE__DEFAULT_DOWNGRADE_CHAIN", "model-x,model-y")
	t.Setenv("OBERWATCH_GATE__ALERT_THRESHOLDS_PCT", "[25, 50, 100]")
	t.Setenv("OBERWATCH_TEST__JUDGE__API_KEY", "secret")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	tests := []struct {
		got  any
		want any
		name string
	}{
		{name: "env overrides scalar", got: cfg.Server.Port, want: 9191},
		{name: "file keeps defaulted host", got: cfg.Server.Host, want: "0.0.0.0"},
		{name: "file overrides upstream provider", got: cfg.Upstream.DefaultProvider, want: ProviderCustom},
		{name: "file overrides custom url", got: cfg.Upstream.Custom.BaseURL, want: "https://llm.example.com"},
		{name: "env overrides string slice", got: cfg.Gate.DefaultDowngradeChain, want: []string{"model-x", "model-y"}},
		{name: "env overrides float slice", got: cfg.Gate.AlertThresholdsPct, want: []float64{25, 50, 100}},
		{name: "env overrides nested string", got: cfg.Test.Judge.APIKey, want: "secret"},
		{name: "file uses defaults for trace sqlite path", got: cfg.Trace.SQLitePath, want: "./oberwatch.db"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.got, tt.want) {
				t.Fatalf("got %#v, want %#v", tt.got, tt.want)
			}
		})
	}
}

func TestLoad_ReturnsErrors(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		envKey     string
		envValue   string
		path       string
		wantSubstr string
	}{
		{
			name:       "missing file",
			path:       filepath.Join(t.TempDir(), "missing.toml"),
			wantSubstr: "open",
		},
		{
			name:       "invalid toml",
			content:    "not = [valid",
			wantSubstr: "parse config",
		},
		{
			name: "validation failure",
			content: `
[server]
port = 0
`,
			wantSubstr: "validate config",
		},
		{
			name:       "invalid env override",
			content:    "",
			envKey:     "OBERWATCH_SERVER__PORT",
			envValue:   "bad",
			wantSubstr: "apply OBERWATCH_SERVER__PORT",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.path
			if path == "" {
				path = filepath.Join(t.TempDir(), "oberwatch.toml")
				if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
			}
			if tt.envKey != "" {
				t.Setenv(tt.envKey, tt.envValue)
			}

			_, err := Load(path)
			if err == nil {
				t.Fatal("Load() error = nil, want non-nil")
			}
			if !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("Load() error = %q, want substring %q", err.Error(), tt.wantSubstr)
			}
		})
	}
}

func TestApplyEnvOverrides_TableDriven(t *testing.T) {
	tests := []struct {
		check   func(Config) any
		want    any
		name    string
		wantErr string
		env     []string
	}{
		{
			name:  "override scalar bool",
			env:   []string{"OBERWATCH_SERVER__DASHBOARD=false"},
			check: func(cfg Config) any { return cfg.Server.Dashboard },
			want:  false,
		},
		{
			name:  "override scalar float",
			env:   []string{"OBERWATCH_GATE__GLOBAL_BUDGET__LIMIT_USD=42.5"},
			check: func(cfg Config) any { return cfg.Gate.GlobalBudget.LimitUSD },
			want:  42.5,
		},
		{
			name:  "override nested string",
			env:   []string{"OBERWATCH_UPSTREAM__OPENAI__BASE_URL=https://proxy.example.com"},
			check: func(cfg Config) any { return cfg.Upstream.OpenAI.BaseURL },
			want:  "https://proxy.example.com",
		},
		{
			name:  "override empty string slice",
			env:   []string{"OBERWATCH_ALERTS__EMAIL__TO="},
			check: func(cfg Config) any { return cfg.Alerts.Email.To },
			want:  []string{},
		},
		{
			name:    "unknown field",
			env:     []string{"OBERWATCH_SERVER__MISSING=value"},
			wantErr: `unknown config key segment "MISSING"`,
		},
		{
			name:    "invalid bool",
			env:     []string{"OBERWATCH_SERVER__DASHBOARD=not-bool"},
			wantErr: "parse bool",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			err := applyEnvOverrides(&cfg, tt.env)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("applyEnvOverrides() error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("applyEnvOverrides() error = %v", err)
			}
			if !reflect.DeepEqual(tt.check(cfg), tt.want) {
				t.Fatalf("got %#v, want %#v", tt.check(cfg), tt.want)
			}
		})
	}
}

func TestFindConfigFile_SearchOrder(t *testing.T) {
	tests := []struct {
		wantRel      string
		name         string
		setupCurrent bool
		setupHome    bool
	}{
		{name: "prefers current directory", setupCurrent: true, setupHome: true, wantRel: "oberwatch.toml"},
		{name: "falls back to home config", setupHome: true, wantRel: filepath.Join(".config", "oberwatch", "oberwatch.toml")},
		{name: "returns empty when none found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origWD, err := os.Getwd()
			if err != nil {
				t.Fatalf("Getwd() error = %v", err)
			}

			workDir := t.TempDir()
			homeDir := t.TempDir()
			t.Setenv("HOME", homeDir)
			if err := os.Chdir(workDir); err != nil {
				t.Fatalf("Chdir() error = %v", err)
			}
			t.Cleanup(func() {
				if err := os.Chdir(origWD); err != nil {
					t.Fatalf("restore cwd: %v", err)
				}
			})

			if tt.setupCurrent {
				if err := os.WriteFile(filepath.Join(workDir, "oberwatch.toml"), []byte(""), 0o644); err != nil {
					t.Fatalf("WriteFile(current) error = %v", err)
				}
			}
			if tt.setupHome {
				homePath := filepath.Join(homeDir, ".config", "oberwatch")
				if err := os.MkdirAll(homePath, 0o755); err != nil {
					t.Fatalf("MkdirAll() error = %v", err)
				}
				if err := os.WriteFile(filepath.Join(homePath, "oberwatch.toml"), []byte(""), 0o644); err != nil {
					t.Fatalf("WriteFile(home) error = %v", err)
				}
			}

			got := FindConfigFile()
			if tt.wantRel == "" {
				if got != "" {
					t.Fatalf("FindConfigFile() = %q, want empty", got)
				}
				return
			}

			var want string
			if tt.setupCurrent {
				want = "./" + tt.wantRel
			} else {
				want = filepath.Join(homeDir, tt.wantRel)
			}
			if got != want {
				t.Fatalf("FindConfigFile() = %q, want %q", got, want)
			}
		})
	}
}

func TestResolveConfigPath(t *testing.T) {
	tests := []struct {
		wantSuffix string
		wantErr    string
		name       string
		explicit   bool
		setupCWD   bool
	}{
		{name: "uses explicit path", explicit: true, wantSuffix: "custom.toml"},
		{name: "finds cwd config", setupCWD: true, wantSuffix: "oberwatch.toml"},
		{name: "returns not found error", wantErr: "no config file found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origWD, getWDErr := os.Getwd()
			if getWDErr != nil {
				t.Fatalf("Getwd() error = %v", getWDErr)
			}

			dir := t.TempDir()
			t.Setenv("HOME", t.TempDir())
			if chdirErr := os.Chdir(dir); chdirErr != nil {
				t.Fatalf("Chdir() error = %v", chdirErr)
			}
			t.Cleanup(func() {
				if restoreErr := os.Chdir(origWD); restoreErr != nil {
					t.Fatalf("restore cwd: %v", restoreErr)
				}
			})

			path := ""
			if tt.explicit {
				path = filepath.Join(dir, "custom.toml")
			}
			if tt.setupCWD {
				if writeErr := os.WriteFile(filepath.Join(dir, "oberwatch.toml"), []byte(""), 0o644); writeErr != nil {
					t.Fatalf("WriteFile() error = %v", writeErr)
				}
			}

			got, err := resolveConfigPath(path)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("resolveConfigPath() error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveConfigPath() error = %v", err)
			}
			if !strings.HasSuffix(got, tt.wantSuffix) {
				t.Fatalf("resolveConfigPath() = %q, want suffix %q", got, tt.wantSuffix)
			}
		})
	}
}

func TestSetSliceValueFromString_TOMLArraySyntax(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	root := reflect.ValueOf(&cfg).Elem()

	stringSlice, err := findFieldByTOMLTag(root.FieldByName("Gate"), "default_downgrade_chain")
	if err != nil {
		t.Fatalf("findFieldByTOMLTag() error = %v", err)
	}
	if setErr := setSliceValueFromString(stringSlice, `["a", "b"]`); setErr != nil {
		t.Fatalf("setSliceValueFromString() error = %v", setErr)
	}
	if !reflect.DeepEqual(cfg.Gate.DefaultDowngradeChain, []string{"a", "b"}) {
		t.Fatalf("got %#v", cfg.Gate.DefaultDowngradeChain)
	}

	floatSlice, err := findFieldByTOMLTag(root.FieldByName("Gate"), "alert_thresholds_pct")
	if err != nil {
		t.Fatalf("findFieldByTOMLTag() error = %v", err)
	}
	if err := setSliceValueFromString(floatSlice, `[10, 20, 30]`); err != nil {
		t.Fatalf("setSliceValueFromString() error = %v", err)
	}
	if !reflect.DeepEqual(cfg.Gate.AlertThresholdsPct, []float64{10, 20, 30}) {
		t.Fatalf("got %#v", cfg.Gate.AlertThresholdsPct)
	}
}
