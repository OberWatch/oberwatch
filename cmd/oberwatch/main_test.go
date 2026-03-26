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
		name     string
		command  []string
		wantName string
	}{
		{name: "serve", command: []string{"serve"}, wantName: "serve"},
		{name: "gate", command: []string{"gate"}, wantName: "gate"},
		{name: "trace", command: []string{"trace"}, wantName: "trace"},
		{name: "test", command: []string{"test"}, wantName: "test"},
		{name: "test run", command: []string{"test", "run"}, wantName: "run"},
		{name: "validate", command: []string{"validate"}, wantName: "validate"},
		{name: "init", command: []string{"init"}, wantName: "init"},
		{name: "version", command: []string{"version"}, wantName: "version"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := newRootCmd()
			cmd, _, err := root.Find(tt.command)
			if err != nil {
				t.Fatalf("Find(%v) error = %v", tt.command, err)
			}
			if cmd == nil {
				t.Fatalf("Find(%v) returned nil command", tt.command)
			}
			if cmd.Name() != tt.wantName {
				t.Fatalf("Find(%v) command = %q, want %q", tt.command, cmd.Name(), tt.wantName)
			}
		})
	}
}

func TestNewRootCmd_DefinesExpectedFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		command []string
		flags   []string
		name    string
	}{
		{
			name:    "root global flags",
			command: []string{},
			flags:   []string{"config", "log-level", "log-format", "version"},
		},
		{
			name:    "serve flags",
			command: []string{"serve"},
			flags:   []string{"port", "host", "admin-token", "no-dashboard", "no-trace", "no-gate"},
		},
		{
			name:    "gate flags",
			command: []string{"gate"},
			flags:   []string{"port", "host", "admin-token"},
		},
		{
			name:    "trace flags",
			command: []string{"trace"},
			flags:   []string{"port", "storage", "db-path", "retention"},
		},
		{
			name:    "test run flags",
			command: []string{"test", "run"},
			flags:   []string{"concurrency", "timeout", "output", "output-file", "fail-fast", "filter", "proxy-url", "judge-model", "judge-key", "dry-run"},
		},
		{
			name:    "validate inherited config flag",
			command: []string{"validate"},
			flags:   []string{"config"},
		},
		{
			name:    "init flags",
			command: []string{"init"},
			flags:   []string{"output", "force"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := newRootCmd()
			cmd := root
			if len(tt.command) > 0 {
				found, _, err := root.Find(tt.command)
				if err != nil {
					t.Fatalf("Find(%v) error = %v", tt.command, err)
				}
				cmd = found
			}

			for _, name := range tt.flags {
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

func TestTraceAndTestRun_FlagParsing(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantErrSub string
	}{
		{
			name: "trace valid flags",
			args: []string{"trace", "--port", "8082", "--storage", "memory", "--retention", "24h"},
		},
		{
			name:       "trace invalid storage",
			args:       []string{"trace", "--storage", "bad-storage"},
			wantErrSub: "--storage must be one of memory, sqlite",
		},
		{
			name: "test run valid flags",
			args: []string{"test", "run", "--concurrency", "8", "--timeout", "45s", "--output", "json", "--filter", "invoice", "scenarios/"},
		},
		{
			name:       "test run invalid output",
			args:       []string{"test", "run", "--output", "xml"},
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
			root.SetArgs(append([]string{"--config", cfgPath}, tt.args...))

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
			want := "oberwatch " + version
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

func writeValidConfig(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "oberwatch.toml")
	if err := os.WriteFile(path, []byte(config.StarterTOML), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}
