package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewInitCmd(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "oberwatch.toml")

	cmd := NewInitCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"--output", path})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "wrote starter config") {
		t.Fatalf("stdout = %q, want success message", stdout.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
}

func TestNewValidateCmd_TableDriven(t *testing.T) {
	tests := []struct {
		args       []string
		content    string
		envKey     string
		envValue   string
		wantErr    string
		wantStdout string
		name       string
		useCWD     bool
	}{
		{
			name:       "valid explicit config",
			content:    StarterTOML,
			args:       []string{"--config"},
			wantStdout: "is valid",
		},
		{
			name: "invalid config",
			content: `
[server]
port = 0
`,
			args:    []string{"--config"},
			wantErr: "validate config",
		},
		{
			name:       "valid with env override",
			content:    StarterTOML,
			args:       []string{"--config"},
			envKey:     "OBERWATCH_SERVER__PORT",
			envValue:   "8181",
			wantStdout: "is valid",
		},
		{
			name:       "searches cwd when config flag omitted",
			content:    StarterTOML,
			wantStdout: "is valid",
			useCWD:     true,
		},
		{
			name:    "no file found",
			wantErr: "no config file found",
			useCWD:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "oberwatch.toml")

			if tt.content != "" {
				if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
			}
			if tt.envKey != "" {
				t.Setenv(tt.envKey, tt.envValue)
			}

			cmd := NewValidateCmd()
			var stdout bytes.Buffer
			cmd.SetOut(&stdout)

			if tt.useCWD {
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
			}

			if len(tt.args) > 0 {
				cmd.SetArgs(append(tt.args, path))
			}

			err := cmd.Execute()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Execute() error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Fatalf("stdout = %q, want substring %q", stdout.String(), tt.wantStdout)
			}
		})
	}
}
