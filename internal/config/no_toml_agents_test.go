package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoad_RejectsTOMLAgentDeclarations proves TOML can never declare or
// register agents: a config file with a [[gate.agents]] table must fail to
// load instead of being silently accepted.
func TestLoad_RejectsTOMLAgentDeclarations(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "oberwatch.toml")
	contents := `
[gate]
enabled = true

[[gate.agents]]
name = "shadow-agent"
limit_usd = 10
period = "daily"
action_on_exceed = "alert"
`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want an error rejecting gate.agents")
	}
	if !strings.Contains(err.Error(), "gate.agents") {
		t.Fatalf("Load() error = %v, want it to mention gate.agents", err)
	}
}
