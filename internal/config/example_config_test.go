package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/BurntSushi/toml"
)

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
	if len(cfg.Gate.APIKeyMap) != 0 {
		t.Fatalf("oberwatch.example.toml declares %d active gate.api_key_map entries, want 0", len(cfg.Gate.APIKeyMap))
	}
}
