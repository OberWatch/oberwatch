package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// exampleAgentNames are the invented agent policies that used to ship as active
// config. A fresh install must not pretend these agents exist.
var exampleAgentNames = []string{"email-agent", "finance-agent"}

func repoFile(t *testing.T, name string) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	return filepath.Join(filepath.Dir(filename), "..", "..", name)
}

func readExampleConfig(t *testing.T) string {
	t.Helper()
	contents, err := os.ReadFile(repoFile(t, "oberwatch.example.toml"))
	if err != nil {
		t.Fatalf("read oberwatch.example.toml: %v", err)
	}
	return string(contents)
}

func TestExampleConfig_HasNoActiveAgentPolicies(t *testing.T) {
	t.Parallel()

	contents := readExampleConfig(t)

	cfg := DefaultConfig()
	if _, err := toml.Decode(contents, &cfg); err != nil {
		t.Fatalf("toml.Decode() error = %v", err)
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if len(cfg.Gate.Agents) != 0 {
		t.Fatalf("oberwatch.example.toml declares %d active gate.agents, want 0", len(cfg.Gate.Agents))
	}
	if len(cfg.Gate.APIKeyMap) != 0 {
		t.Fatalf("oberwatch.example.toml declares %d active gate.api_key_map entries, want 0", len(cfg.Gate.APIKeyMap))
	}
}

func TestExampleConfig_KeepsAgentPolicySyntaxCommented(t *testing.T) {
	t.Parallel()

	contents := readExampleConfig(t)
	for _, line := range strings.Split(contents, "\n") {
		trimmed := strings.TrimSpace(line)
		for _, name := range exampleAgentNames {
			if strings.Contains(trimmed, name) && !strings.HasPrefix(trimmed, "#") {
				t.Fatalf("oberwatch.example.toml mentions %q outside a comment: %q", name, trimmed)
			}
		}
	}

	if !strings.Contains(contents, "# [[gate.agents]]") {
		t.Fatal("oberwatch.example.toml should keep the per-agent policy syntax as a commented sample")
	}
}
