package dashboard

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewHandler_ServesStaticAndSPAFallback(t *testing.T) {
	t.Parallel()

	if !EmbeddedIndexExists() {
		t.Fatal("embedded index.html is missing from static assets")
	}

	handler, err := NewHandler()
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}

	//nolint:govet // test table keeps a readable declaration order.
	//nolint:govet // Keep table fields grouped for readable HTTP expectations.
	//nolint:govet // Keep table fields grouped for readable HTTP expectations.
	tests := []struct {
		wantStatus   int
		name         string
		method       string
		path         string
		wantContains string
	}{
		{
			name:         "root serves index",
			method:       http.MethodGet,
			path:         "/",
			wantStatus:   http.StatusOK,
			wantContains: "<!doctype html>",
		},
		{
			name:         "spa fallback serves index for unknown path",
			method:       http.MethodGet,
			path:         "/agents",
			wantStatus:   http.StatusOK,
			wantContains: "<!doctype html>",
		},
		{
			name:       "asset path serves bytes",
			method:     http.MethodGet,
			path:       "/fonts/inter-400.woff2",
			wantStatus: http.StatusOK,
		},
		{
			name:       "logo asset serves bytes",
			method:     http.MethodGet,
			path:       "/logo-white.svg",
			wantStatus: http.StatusOK,
		},
		{
			name:       "non-get returns not found",
			method:     http.MethodPost,
			path:       "/",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantContains != "" && !strings.Contains(strings.ToLower(rec.Body.String()), strings.ToLower(tt.wantContains)) {
				t.Fatalf("body = %q, want substring %q", rec.Body.String(), tt.wantContains)
			}
		})
	}
}

func TestOverlayFS_Open(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		primaryFiles map[string]string
		fallbackFile string
		path         string
		wantBody     string
		wantErr      bool
	}{
		{
			name:         "reads from primary first",
			primaryFiles: map[string]string{"logo.svg": "primary"},
			fallbackFile: "secondary",
			path:         "logo.svg",
			wantBody:     "primary",
		},
		{
			name:         "falls back to secondary",
			primaryFiles: map[string]string{"index.html": "ok"},
			fallbackFile: "secondary",
			path:         "logo.svg",
			wantBody:     "secondary",
		},
		{
			name:         "returns not exist when missing everywhere",
			primaryFiles: map[string]string{"index.html": "ok"},
			fallbackFile: "",
			path:         "missing.svg",
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			primaryDir := t.TempDir()
			secondaryDir := t.TempDir()
			for name, body := range tt.primaryFiles {
				writeFile(t, filepath.Join(primaryDir, name), body)
			}
			if tt.fallbackFile != "" {
				writeFile(t, filepath.Join(secondaryDir, "logo.svg"), tt.fallbackFile)
			}

			files := overlayFS{
				primary:   os.DirFS(primaryDir),
				secondary: os.DirFS(secondaryDir),
			}

			file, err := files.Open(tt.path)
			if tt.wantErr {
				if err == nil {
					t.Fatal("Open() error = nil, want non-nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			t.Cleanup(func() {
				_ = file.Close()
			})

			data, readErr := fs.ReadFile(files, tt.path)
			if readErr != nil {
				t.Fatalf("ReadFile() error = %v", readErr)
			}
			if string(data) != tt.wantBody {
				t.Fatalf("body = %q, want %q", string(data), tt.wantBody)
			}
		})
	}
}

func TestLocalBuildHandler_ServesBuildAndStaticFiles(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	writeFile(t, filepath.Join(tempDir, "dashboard/svelte/build/index.html"), "<!doctype html><html>build</html>")
	writeFile(t, filepath.Join(tempDir, "dashboard/svelte/static/logo-white.svg"), "<svg>logo</svg>")

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})

	handler, ok := localBuildHandler()
	if !ok {
		t.Fatal("localBuildHandler() ok = false, want true")
	}

	//nolint:govet // Keep table fields grouped for readable HTTP expectations.
	tests := []struct {
		wantStatus   int
		name         string
		path         string
		wantContains string
	}{
		{name: "root serves built index", path: "/", wantStatus: http.StatusOK, wantContains: "build"},
		{name: "static logo served from fallback dir", path: "/logo-white.svg", wantStatus: http.StatusOK, wantContains: "logo"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if !strings.Contains(rec.Body.String(), tt.wantContains) {
				t.Fatalf("body = %q, want substring %q", rec.Body.String(), tt.wantContains)
			}
		})
	}
}

func writeFile(t *testing.T, path string, contents string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}
