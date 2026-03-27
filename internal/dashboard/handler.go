package dashboard

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

// NewHandler serves embedded dashboard assets with SPA fallback to index.html.
func NewHandler() (http.Handler, error) {
	if localHandler, ok := localBuildHandler(); ok {
		return localHandler, nil
	}

	embeddedFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, fmt.Errorf("open embedded dashboard subtree: %w", err)
	}

	return newSPAHandler(embeddedFS), nil
}

func localBuildHandler() (http.Handler, bool) {
	const localBuildDir = "dashboard/svelte/build"
	if _, err := os.Stat(localBuildDir); err != nil {
		return nil, false
	}
	return newSPAHandler(os.DirFS(localBuildDir)), true
}

func newSPAHandler(assets fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}

		cleanPath := path.Clean("/" + r.URL.Path)
		relPath := strings.TrimPrefix(cleanPath, "/")
		if relPath == "" {
			relPath = "index.html"
		}

		if assetExists(assets, relPath) {
			fileServer.ServeHTTP(w, r)
			return
		}

		indexData, readErr := fs.ReadFile(assets, "index.html")
		if readErr != nil {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, writeErr := w.Write(indexData); writeErr != nil {
			return
		}
	})
}

func assetExists(files fs.FS, relPath string) bool {
	info, err := fs.Stat(files, relPath)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// EmbeddedIndexExists reports whether index.html exists in embedded static assets.
func EmbeddedIndexExists() bool {
	_, err := fs.ReadFile(staticFiles, "static/index.html")
	return err == nil
}
