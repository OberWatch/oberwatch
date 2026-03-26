package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestGenerateStarter_TableDriven(t *testing.T) {
	tests := []struct {
		name      string
		wantErr   string
		setupFile bool
	}{
		{name: "writes new file"},
		{name: "rejects existing file", setupFile: true, wantErr: "refusing to overwrite"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "nested", "oberwatch.toml")
			if tt.setupFile {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("MkdirAll() error = %v", err)
				}
				if err := os.WriteFile(path, []byte("existing"), 0o644); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
			}

			err := GenerateStarter(path)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("GenerateStarter() error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("GenerateStarter() error = %v", err)
			}

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			if string(data) != StarterTOML {
				t.Fatalf("generated file content mismatch")
			}
		})
	}
}

func TestStarterTOML_IsValidAndPassesValidation(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	if _, err := toml.Decode(StarterTOML, &cfg); err != nil {
		t.Fatalf("toml.Decode() error = %v", err)
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestStarterTOML_ContainsExpectedSections(t *testing.T) {
	t.Parallel()

	sections := []string{
		"[server]",
		"[upstream]",
		"[upstream.custom]",
		"[gate]",
		"[gate.global_budget]",
		"[alerts.email]",
		"[trace]",
		"[test.judge]",
		"[[pricing]]",
		"OBERWATCH_SERVER__PORT",
	}

	for _, section := range sections {
		t.Run(section, func(t *testing.T) {
			t.Parallel()
			if !strings.Contains(StarterTOML, section) {
				t.Fatalf("StarterTOML missing %q", section)
			}
		})
	}
}
