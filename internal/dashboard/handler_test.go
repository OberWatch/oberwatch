package dashboard

import (
	"net/http"
	"net/http/httptest"
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
