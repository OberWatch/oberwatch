package config

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestTaskBudget_DefaultIsDisabled(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	if cfg.Gate.TaskBudgetUSD != 0 {
		t.Fatalf("Gate.TaskBudgetUSD = %v, want 0 (disabled)", cfg.Gate.TaskBudgetUSD)
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate(default) error = %v", err)
	}
}

func TestTaskBudget_Validation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		mutate     func(*Config)
		wantSubstr string
	}{
		{
			name: "gate task budget must be non-negative",
			mutate: func(cfg *Config) {
				cfg.Gate.TaskBudgetUSD = -0.01
			},
			wantSubstr: "gate.task_budget_usd must be non-negative",
		},
		{
			name: "zero and positive values are valid",
			mutate: func(cfg *Config) {
				cfg.Gate.TaskBudgetUSD = 5
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := DefaultConfig()
			tt.mutate(&cfg)
			err := Validate(cfg)
			if tt.wantSubstr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("Validate() error = %v, want substring %q", err, tt.wantSubstr)
			}
		})
	}
}

func TestTaskBudget_TOMLAndEnvOverride(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	snippet := `
[gate]
task_budget_usd = 2.5
`
	if _, err := toml.Decode(snippet, &cfg); err != nil {
		t.Fatalf("toml.Decode() error = %v", err)
	}
	if cfg.Gate.TaskBudgetUSD != 2.5 {
		t.Fatalf("Gate.TaskBudgetUSD = %v, want 2.5", cfg.Gate.TaskBudgetUSD)
	}

	if err := applyEnvOverrides(&cfg, []string{"OBERWATCH_GATE__TASK_BUDGET_USD=4"}); err != nil {
		t.Fatalf("applyEnvOverrides() error = %v", err)
	}
	if cfg.Gate.TaskBudgetUSD != 4 {
		t.Fatalf("Gate.TaskBudgetUSD after env override = %v, want 4", cfg.Gate.TaskBudgetUSD)
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestTaskBudget_DocumentedInTemplates(t *testing.T) {
	t.Parallel()

	if !strings.Contains(readExampleConfig(t), "task_budget_usd = 0") {
		t.Fatal("oberwatch.example.toml should document gate.task_budget_usd")
	}
	if !strings.Contains(StarterTOML, "task_budget_usd = 0") {
		t.Fatal("init template should document gate.task_budget_usd")
	}
}
