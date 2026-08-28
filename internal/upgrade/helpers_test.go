package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// tarEntry is one member of a test archive.
type tarEntry struct {
	Name     string
	Body     string
	Linkname string
	Mode     int64
	Typeflag byte
}

// binaryEntry builds the archive member the applier extracts. The body is a
// shell script so the extracted file really runs and really prints a version,
// which is what the applier's pre-swap check reads.
func binaryEntry(tag string) tarEntry {
	return tarEntry{
		Name:     BinaryName,
		Body:     "#!/bin/sh\necho \"oberwatch " + tag + "\"\n",
		Mode:     0o755,
		Typeflag: tar.TypeReg,
	}
}

// releaseEntries is what a real release archive carries: the binary plus the
// documentation files goreleaser adds.
func releaseEntries(tag string) []tarEntry {
	return []tarEntry{
		{Name: "LICENSE", Body: "license text", Mode: 0o644, Typeflag: tar.TypeReg},
		{Name: "README.md", Body: "readme text", Mode: 0o644, Typeflag: tar.TypeReg},
		binaryEntry(tag),
	}
}

// tarGzBytes builds a gzip-compressed tar archive in memory.
func tarGzBytes(t *testing.T, entries []tarEntry) []byte {
	t.Helper()

	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)

	for _, entry := range entries {
		typeflag := entry.Typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		mode := entry.Mode
		if mode == 0 {
			mode = 0o644
		}
		header := &tar.Header{
			Name:     entry.Name,
			Mode:     mode,
			Size:     int64(len(entry.Body)),
			Typeflag: typeflag,
			Linkname: entry.Linkname,
		}
		if typeflag != tar.TypeReg {
			header.Size = 0
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatalf("WriteHeader(%q) error = %v", entry.Name, err)
		}
		if typeflag == tar.TypeReg && entry.Body != "" {
			if _, err := tarWriter.Write([]byte(entry.Body)); err != nil {
				t.Fatalf("Write(%q) error = %v", entry.Name, err)
			}
		}
	}

	if err := tarWriter.Close(); err != nil {
		t.Fatalf("tar Close() error = %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("gzip Close() error = %v", err)
	}
	return buffer.Bytes()
}

// writeTarGz writes a gzip-compressed tar archive to disk.
func writeTarGz(t *testing.T, destinationPath string, entries []tarEntry) []byte {
	t.Helper()

	data := tarGzBytes(t, entries)
	if err := os.WriteFile(destinationPath, data, 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", destinationPath, err)
	}
	return data
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()

	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q) error = %v", raw, err)
	}
	return parsed
}

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// checksumsDocument renders a "sha256sum" style document for the given assets.
func checksumsDocument(assets map[string][]byte) []byte {
	names := make([]string, 0, len(assets))
	for name := range assets {
		names = append(names, name)
	}
	sort.Strings(names)

	var builder strings.Builder
	for _, name := range names {
		fmt.Fprintf(&builder, "%s  %s\n", digestOf(assets[name]), name)
	}
	return []byte(builder.String())
}

// releaseServer serves release assets the way the download prefix expects:
// "<base>/<tag>/<asset>".
//
//nolint:govet // Keep the stand-in grouped by server, then recorded requests.
type releaseServer struct {
	Server  *httptest.Server
	BaseURL string

	// mu guards requests, which is appended from the handler goroutine and read
	// from the test goroutine.
	mu       sync.Mutex
	requests []string
}

// RequestedPaths returns the asset paths that were requested, so a test can
// assert what was and was not fetched.
func (r *releaseServer) RequestedPaths() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.requests...)
}

// newReleaseServer starts a local stand-in for the release download host. The
// assets are keyed by tag and then by asset name.
func newReleaseServer(t *testing.T, assets map[string]map[string][]byte) *releaseServer {
	t.Helper()

	release := &releaseServer{}
	release.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("artifact download sent an Authorization header %q; downloads must be credential-free", got)
		}

		trimmed := strings.TrimPrefix(r.URL.Path, "/download/")
		release.mu.Lock()
		release.requests = append(release.requests, trimmed)
		release.mu.Unlock()

		tag, name := path.Split(trimmed)
		tag = strings.Trim(tag, "/")

		body, ok := assets[tag][name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	}))
	t.Cleanup(release.Server.Close)

	release.BaseURL = release.Server.URL + "/download/"
	return release
}

// safeDir creates a directory that is not group- or world-writable.
//
// t.TempDir() inherits the process umask, which on some machines leaves the
// directory group-writable; the guards under test refuse exactly that, so the
// tests create their own directories with explicit modes.
func safeDir(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "safe")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("Mkdir(%q) error = %v", dir, err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("Chmod(%q) error = %v", dir, err)
	}
	return dir
}

// stateDir creates a handoff directory with the permissions the installer gives
// it.
func stateDir(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "upgrade")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("Mkdir(%q) error = %v", dir, err)
	}
	return dir
}
