package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func chdirForTest(t *testing.T, dir string) {
	t.Helper()

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

func TestValidateFile_TableDriven(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		wantSource   string
		wantErrSubs  []string
		wantWarnSubs []string
		explicit     bool
		writeFile    bool
		wantNotFound bool
	}{
		{
			name:       "explicit valid starter has no warnings",
			content:    StarterTOML,
			explicit:   true,
			writeFile:  true,
			wantSource: SourceFlag,
		},
		{
			name:         "explicit missing file reports not found with path",
			explicit:     true,
			wantErrSubs:  []string{"not found", "missing.toml"},
			wantNotFound: true,
		},
		{
			name:        "malformed toml reports parse error with line",
			content:     "[server]\nport = \n",
			explicit:    true,
			writeFile:   true,
			wantErrSubs: []string{"parse config", "line 2"},
		},
		{
			name:        "semantic error names the offending key",
			content:     "[server]\nport = 0\n",
			explicit:    true,
			writeFile:   true,
			wantErrSubs: []string{"validate config", "server.port"},
		},
		{
			name:        "unknown key is reported as parse error",
			content:     "[server]\nprot = 8080\n",
			explicit:    true,
			writeFile:   true,
			wantErrSubs: []string{"parse config", "prot"},
		},
		{
			name:         "admin token set warns that auth is session based",
			content:      "[server]\nadmin_token = \"legacy\"\n",
			explicit:     true,
			writeFile:    true,
			wantWarnSubs: []string{"server.admin_token", "session"},
			wantSource:   SourceFlag,
		},
		{
			name:         "dashboard disabled warns about setup endpoint",
			content:      "[server]\ndashboard = false\n",
			explicit:     true,
			writeFile:    true,
			wantWarnSubs: []string{"server.dashboard", "/_oberwatch/api/v1/setup"},
			wantSource:   SourceFlag,
		},
		{
			name:       "cwd search resolves ./oberwatch.toml",
			content:    StarterTOML,
			writeFile:  true,
			wantSource: SourceSearch,
		},
		{
			name:         "cwd search with no file lists search order",
			wantErrSubs:  []string{"no config file found", "./oberwatch.toml", "/etc/oberwatch/oberwatch.toml"},
			wantNotFound: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "oberwatch.toml")
			if tt.writeFile {
				if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
			}

			arg := ""
			if tt.explicit {
				arg = path
				if !tt.writeFile {
					arg = filepath.Join(dir, "missing.toml")
				}
			} else {
				// HOME is redirected so the user's real config cannot leak into the search.
				t.Setenv("HOME", t.TempDir())
				chdirForTest(t, dir)
			}

			report, err := ValidateFile(arg)
			if len(tt.wantErrSubs) > 0 || tt.wantNotFound {
				if err == nil {
					t.Fatalf("ValidateFile() error = nil, want error containing %v", tt.wantErrSubs)
				}
				for _, sub := range tt.wantErrSubs {
					if !strings.Contains(err.Error(), sub) {
						t.Fatalf("ValidateFile() error = %q, want substring %q", err.Error(), sub)
					}
				}
				var notFound *NotFoundError
				if got := errors.As(err, &notFound); got != tt.wantNotFound {
					t.Fatalf("errors.As(NotFoundError) = %v, want %v (err = %v)", got, tt.wantNotFound, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateFile() error = %v", err)
			}

			wantPath := path
			if !tt.explicit {
				wantPath = "./oberwatch.toml"
			}
			if report.Path != wantPath {
				t.Fatalf("report.Path = %q, want %q", report.Path, wantPath)
			}
			if report.Source != tt.wantSource {
				t.Fatalf("report.Source = %q, want %q", report.Source, tt.wantSource)
			}
			if len(tt.wantWarnSubs) == 0 && len(report.Warnings) != 0 {
				t.Fatalf("report.Warnings = %v, want none", report.Warnings)
			}
			joined := strings.Join(report.Warnings, "\n")
			for _, sub := range tt.wantWarnSubs {
				if !strings.Contains(joined, sub) {
					t.Fatalf("report.Warnings = %v, want substring %q", report.Warnings, sub)
				}
			}
		})
	}
}

func TestValidateFile_ExplicitDirectoryIsRejected(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	_, err := ValidateFile(dir)
	if err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("ValidateFile(dir) error = %v, want 'is a directory'", err)
	}
}

func TestWriteStarter_TableDriven(t *testing.T) {
	tests := []struct {
		name        string
		preexisting string
		wantErr     string
		wantContent string
		force       bool
	}{
		{name: "creates nested new file", wantContent: StarterTOML},
		{name: "refuses to truncate existing file without force", preexisting: "keep me", wantErr: "refusing to overwrite", wantContent: "keep me"},
		{name: "force overwrites existing file", preexisting: "old", force: true, wantContent: StarterTOML},
		{name: "force creates missing nested file", force: true, wantContent: StarterTOML},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "nested", "oberwatch.toml")
			if tt.preexisting != "" {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("MkdirAll() error = %v", err)
				}
				if err := os.WriteFile(path, []byte(tt.preexisting), 0o644); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
			}

			err := WriteStarter(path, tt.force)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("WriteStarter() error = %v, want substring %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("WriteStarter() error = %v", err)
			}

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			if string(data) != tt.wantContent {
				t.Fatalf("file content = %q, want %q", truncateForTest(string(data)), truncateForTest(tt.wantContent))
			}
		})
	}
}

func TestWriteStarter_OutputRoundTripsThroughValidateFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "custom", "config.toml")
	if err := WriteStarter(path, false); err != nil {
		t.Fatalf("WriteStarter() error = %v", err)
	}
	report, err := ValidateFile(path)
	if err != nil {
		t.Fatalf("ValidateFile() error = %v", err)
	}
	if report.Path != path {
		t.Fatalf("report.Path = %q, want %q", report.Path, path)
	}
	if len(report.Warnings) != 0 {
		t.Fatalf("starter config produced warnings: %v", report.Warnings)
	}
}

func TestInitSuccessMessage_NamesPathAndServeStep(t *testing.T) {
	t.Parallel()

	got := InitSuccessMessage("./custom/oberwatch.toml")
	want := "wrote starter config to ./custom/oberwatch.toml\nnext: oberwatch serve --config ./custom/oberwatch.toml\n"
	if got != want {
		t.Fatalf("InitSuccessMessage() = %q, want %q", got, want)
	}
}

func TestStarterTOML_DoesNotClaimBearerOnlyManagementAuth(t *testing.T) {
	t.Parallel()

	for _, forbidden := range []string{"REQUIRED in production", "management API is disabled", "Bearer token required"} {
		if strings.Contains(StarterTOML, forbidden) {
			t.Fatalf("StarterTOML still contains obsolete auth claim %q", forbidden)
		}
	}
	if !strings.Contains(StarterTOML, "session") {
		t.Fatal("StarterTOML should explain that management auth is session-based")
	}
}

func TestExampleConfig_DoesNotClaimBearerOnlyManagementAuth(t *testing.T) {
	t.Parallel()

	contents := readExampleConfig(t)
	for _, forbidden := range []string{"Bearer token required", "REQUIRED in production"} {
		if strings.Contains(contents, forbidden) {
			t.Fatalf("oberwatch.example.toml still contains obsolete auth claim %q", forbidden)
		}
	}
}

func truncateForTest(value string) string {
	if len(value) > 60 {
		return value[:60] + "..."
	}
	return value
}
