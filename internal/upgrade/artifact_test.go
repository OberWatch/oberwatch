package upgrade

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseChecksums(t *testing.T) {
	t.Parallel()

	validDigest := strings.Repeat("ab", 32)
	otherDigest := strings.Repeat("cd", 32)

	//nolint:govet // Keep table fields grouped by document, then expectation.
	tests := []struct {
		name    string
		raw     string
		want    map[string]string
		wantErr bool
	}{
		{
			name: "goreleaser style document",
			raw:  validDigest + "  oberwatch_0.1.4_linux_amd64.tar.gz\n" + otherDigest + "  oberwatch_0.1.4_linux_arm64.tar.gz\n",
			want: map[string]string{
				"oberwatch_0.1.4_linux_amd64.tar.gz": validDigest,
				"oberwatch_0.1.4_linux_arm64.tar.gz": otherDigest,
			},
		},
		{
			name: "binary mode marker is accepted",
			raw:  validDigest + " *oberwatch_0.1.4_linux_amd64.tar.gz\n",
			want: map[string]string{"oberwatch_0.1.4_linux_amd64.tar.gz": validDigest},
		},
		{
			name: "uppercase digests are normalised",
			raw:  strings.ToUpper(validDigest) + "  asset.tar.gz\n",
			want: map[string]string{"asset.tar.gz": validDigest},
		},
		{
			name: "blank lines are skipped",
			raw:  "\n\n" + validDigest + "  asset.tar.gz\n\n",
			want: map[string]string{"asset.tar.gz": validDigest},
		},
		{
			name: "a repeated name with the same digest is fine",
			raw:  validDigest + "  asset.tar.gz\n" + validDigest + "  asset.tar.gz\n",
			want: map[string]string{"asset.tar.gz": validDigest},
		},

		{name: "empty document", raw: "", wantErr: true},
		{name: "whitespace only document", raw: "   \n\t\n", wantErr: true},
		{name: "truncated digest", raw: "abc  asset.tar.gz\n", wantErr: true},
		{name: "non hex digest", raw: strings.Repeat("zz", 32) + "  asset.tar.gz\n", wantErr: true},
		{name: "digest without a name", raw: validDigest + "\n", wantErr: true},
		{name: "name without a digest", raw: "asset.tar.gz\n", wantErr: true},
		{name: "extra field", raw: validDigest + "  asset.tar.gz  extra\n", wantErr: true},
		{name: "a name that is a path is refused", raw: validDigest + "  ../../etc/passwd\n", wantErr: true},
		{name: "a name with a forward slash is refused", raw: validDigest + "  dir/asset.tar.gz\n", wantErr: true},
		{name: "a name with a backslash is refused", raw: validDigest + `  dir\asset.tar.gz` + "\n", wantErr: true},
		{name: "an absolute name is refused", raw: validDigest + "  /usr/local/bin/oberwatch\n", wantErr: true},
		{name: "a dot name is refused", raw: validDigest + "  ..\n", wantErr: true},
		{name: "the same name with two digests is refused", raw: validDigest + "  asset.tar.gz\n" + otherDigest + "  asset.tar.gz\n", wantErr: true},
		{name: "an html error page is refused", raw: "<html><body>404</body></html>\n", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseChecksums([]byte(tt.raw))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseChecksums() = %v, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseChecksums() error = %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("ParseChecksums() = %v, want %v", got, tt.want)
			}
			for name, digest := range tt.want {
				if got[name] != digest {
					t.Fatalf("ParseChecksums()[%q] = %q, want %q", name, got[name], digest)
				}
			}
		})
	}
}

func TestParseChecksums_BoundsTheEntryCount(t *testing.T) {
	t.Parallel()

	var builder strings.Builder
	for index := 0; index <= maxChecksumLines; index++ {
		builder.WriteString(strings.Repeat("ab", 32))
		builder.WriteString("  asset-")
		builder.WriteString(strings.Repeat("x", 1))
		builder.WriteString(string(rune('a' + index%26)))
		builder.WriteString(string(rune('a' + (index/26)%26)))
		builder.WriteString(string(rune('a' + (index/676)%26)))
		builder.WriteString(".tar.gz\n")
	}

	if _, err := ParseChecksums([]byte(builder.String())); err == nil {
		t.Fatal("ParseChecksums() accepted a document with more entries than the bound allows")
	}
}

func TestVerifyDigest(t *testing.T) {
	t.Parallel()

	digest := digestOf([]byte("payload"))

	tests := []struct {
		name     string
		expected string
		actual   string
		wantErr  bool
	}{
		{name: "match", expected: digest, actual: digest},
		{name: "case insensitive match", expected: strings.ToUpper(digest), actual: digest},
		{name: "padded match", expected: "  " + digest + "\n", actual: digest},
		{name: "mismatch", expected: digest, actual: digestOf([]byte("other")), wantErr: true},
		{name: "empty expected", expected: "", actual: digest, wantErr: true},
		{name: "empty actual", expected: digest, actual: "", wantErr: true},
		{name: "truncated expected", expected: digest[:20], actual: digest, wantErr: true},
		{name: "non hex expected", expected: strings.Repeat("zz", 32), actual: digest, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := VerifyDigest(tt.expected, tt.actual)
			if tt.wantErr != (err != nil) {
				t.Fatalf("VerifyDigest() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, ErrChecksumMismatch) {
				t.Fatalf("VerifyDigest() error = %v, want ErrChecksumMismatch", err)
			}
		})
	}
}

func TestOpenStagedArchive(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	payload := []byte("release archive bytes")
	archive := filepath.Join(root, "archive.tar.gz")
	if err := os.WriteFile(archive, payload, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	file, err := OpenStagedArchive(archive)
	if err != nil {
		t.Fatalf("OpenStagedArchive() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	empty := filepath.Join(root, "empty.tar.gz")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := OpenStagedArchive(empty); err == nil {
		t.Error("OpenStagedArchive() accepted an empty file")
	}

	link := filepath.Join(root, "link.tar.gz")
	if err := os.Symlink(archive, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	if _, err := OpenStagedArchive(link); err == nil {
		t.Error("OpenStagedArchive() followed a symlink; a link planted in the handoff directory must not be opened")
	}

	if _, err := OpenStagedArchive(filepath.Join(root, "missing.tar.gz")); err == nil {
		t.Error("OpenStagedArchive() accepted a missing file")
	}
	if _, err := OpenStagedArchive(root); err == nil {
		t.Error("OpenStagedArchive() accepted a directory")
	}
}

// The staged archive is read exactly once, and the handle stays valid for that
// one read even if the path it came from is replaced afterwards.
func TestOpenStagedArchive_ReadsTheFileItOpenedNotThePath(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	payload := []byte("release archive bytes")
	archivePath := filepath.Join(root, "archive.tar.gz")
	if err := os.WriteFile(archivePath, payload, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	file, err := OpenStagedArchive(archivePath)
	if err != nil {
		t.Fatalf("OpenStagedArchive() error = %v", err)
	}
	defer func() { _ = file.Close() }()

	// Renaming another file over the path is how a staged archive gets replaced.
	// The open handle keeps reading the file it was given.
	replacement := filepath.Join(root, "replacement.tar.gz")
	if writeErr := os.WriteFile(replacement, []byte("attacker payload"), 0o600); writeErr != nil {
		t.Fatalf("WriteFile() error = %v", writeErr)
	}
	if renameErr := os.Rename(replacement, archivePath); renameErr != nil {
		t.Fatalf("Rename() error = %v", renameErr)
	}

	read, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(read) != string(payload) {
		t.Fatalf("the handle read back %q, want the bytes that were opened %q", read, payload)
	}
}

func TestFetcher_DownloadArchive_VerifiesBeforeStaging(t *testing.T) {
	t.Parallel()

	version := mustParseVersion(t, "v0.1.4")
	platform := Platform{OS: "linux", Arch: "amd64"}
	archiveName, err := ArchiveName(version, platform)
	if err != nil {
		t.Fatalf("ArchiveName() error = %v", err)
	}

	archive := tarGzBytes(t, releaseEntries("v0.1.4"))
	assets := map[string][]byte{archiveName: archive}
	release := newReleaseServer(t, map[string]map[string][]byte{
		"v0.1.4": {
			archiveName:   archive,
			ChecksumsName: checksumsDocument(assets),
		},
	})

	dir := stateDir(t)
	fetcher := &Fetcher{BaseURL: release.BaseURL, HTTPClient: newArtifactClient()}

	path, err := fetcher.DownloadArchive(context.Background(), version, platform, dir)
	if err != nil {
		t.Fatalf("DownloadArchive() error = %v", err)
	}
	if got := filepath.Base(path); got != archiveName {
		t.Fatalf("DownloadArchive() = %q, want the archive to be staged as %q", path, archiveName)
	}

	staged, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if digestOf(staged) != digestOf(archive) {
		t.Fatal("staged archive does not match what the release published")
	}

	assertNoPartialFiles(t, dir)
}

func TestFetcher_DownloadArchive_RefusesAndRemovesAMismatchedDownload(t *testing.T) {
	t.Parallel()

	version := mustParseVersion(t, "v0.1.4")
	platform := Platform{OS: "linux", Arch: "amd64"}
	archiveName, err := ArchiveName(version, platform)
	if err != nil {
		t.Fatalf("ArchiveName() error = %v", err)
	}

	// The checksums document describes the genuine archive; the download host
	// serves a different one. This is the tampered-artifact case.
	genuine := tarGzBytes(t, releaseEntries("v0.1.4"))
	tampered := tarGzBytes(t, []tarEntry{binaryEntry("v0.1.4"), {Name: "payload", Body: "attacker", Typeflag: 0}})

	release := newReleaseServer(t, map[string]map[string][]byte{
		"v0.1.4": {
			archiveName:   tampered,
			ChecksumsName: checksumsDocument(map[string][]byte{archiveName: genuine}),
		},
	})

	dir := stateDir(t)
	fetcher := &Fetcher{BaseURL: release.BaseURL, HTTPClient: newArtifactClient()}

	if _, err := fetcher.DownloadArchive(context.Background(), version, platform, dir); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("DownloadArchive() error = %v, want ErrChecksumMismatch", err)
	}

	if _, err := os.Stat(filepath.Join(dir, archiveName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a mismatched download was left staged under the name the applier looks for")
	}
	assertNoPartialFiles(t, dir)
}

func TestFetcher_DownloadArchive_Failures(t *testing.T) {
	t.Parallel()

	version := mustParseVersion(t, "v0.1.4")
	platform := Platform{OS: "linux", Arch: "amd64"}
	archiveName, err := ArchiveName(version, platform)
	if err != nil {
		t.Fatalf("ArchiveName() error = %v", err)
	}
	archive := tarGzBytes(t, releaseEntries("v0.1.4"))

	//nolint:govet // Keep table fields grouped by URL, then expectation.
	tests := []struct {
		name    string
		assets  map[string][]byte
		wantErr error
	}{
		{
			name:    "no checksums document at all",
			assets:  map[string][]byte{archiveName: archive},
			wantErr: ErrArtifactUnavailable,
		},
		{
			name: "the archive is not listed in the checksums",
			assets: map[string][]byte{
				archiveName:   archive,
				ChecksumsName: checksumsDocument(map[string][]byte{"oberwatch_0.1.4_linux_arm64.tar.gz": archive}),
			},
			wantErr: ErrChecksumMissing,
		},
		{
			name: "the archive is missing from the release",
			assets: map[string][]byte{
				ChecksumsName: checksumsDocument(map[string][]byte{archiveName: archive}),
			},
			wantErr: ErrArtifactUnavailable,
		},
		{
			name: "a malformed checksums document",
			assets: map[string][]byte{
				archiveName:   archive,
				ChecksumsName: []byte("<html>not a checksums document</html>"),
			},
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			release := newReleaseServer(t, map[string]map[string][]byte{"v0.1.4": tt.assets})
			dir := stateDir(t)
			fetcher := &Fetcher{BaseURL: release.BaseURL, HTTPClient: newArtifactClient()}

			_, err := fetcher.DownloadArchive(context.Background(), version, platform, dir)
			if err == nil {
				t.Fatal("DownloadArchive() succeeded, want an error")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("DownloadArchive() error = %v, want %v", err, tt.wantErr)
			}

			entries, readErr := os.ReadDir(dir)
			if readErr != nil {
				t.Fatalf("ReadDir() error = %v", readErr)
			}
			for _, entry := range entries {
				if entry.Name() == archiveName {
					t.Fatal("a failed download left an archive staged under the name the applier looks for")
				}
			}
		})
	}
}

func TestFetcher_Get_BoundsTheChecksumsDocument(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", MaxChecksumsBytes+1)))
	}))
	defer server.Close()

	fetcher := &Fetcher{BaseURL: server.URL + "/download/", HTTPClient: newArtifactClient()}
	_, err := fetcher.Checksums(context.Background(), mustParseVersion(t, "v0.1.4"))
	if !errors.Is(err, ErrArtifactTooLarge) {
		t.Fatalf("Checksums() error = %v, want ErrArtifactTooLarge", err)
	}
}

func TestFetcher_RefusesARedirectOffTheReleaseHosts(t *testing.T) {
	t.Parallel()

	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("attacker payload"))
	}))
	defer elsewhere.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusFound)
	}))
	defer server.Close()

	fetcher := &Fetcher{BaseURL: server.URL + "/download/", HTTPClient: newArtifactClient()}
	if _, err := fetcher.Checksums(context.Background(), mustParseVersion(t, "v0.1.4")); err == nil {
		t.Fatal("Checksums() followed a redirect to a host that is not a release host")
	}
}

func TestRequireArtifactURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "release host", raw: "https://github.com/OberWatch/oberwatch/releases/download/v0.1.4/x.tar.gz"},
		{name: "release api host", raw: "https://api.github.com/repos/OberWatch/oberwatch/releases/latest"},
		{name: "release asset cdn", raw: "https://objects.githubusercontent.com/some/object"},
		{name: "release asset cdn alternate", raw: "https://release-assets.githubusercontent.com/some/object"},

		{name: "plain http is refused", raw: "http://github.com/x", wantErr: true},
		{name: "another host is refused", raw: "https://attacker.test/x", wantErr: true},
		{name: "a lookalike suffix is refused", raw: "https://githubusercontent.com.attacker.test/x", wantErr: true},
		{name: "the bare cdn domain is refused", raw: "https://githubusercontent.com/x", wantErr: true},
		{name: "a host prefix trick is refused", raw: "https://evilgithub.com/x", wantErr: true},
		{name: "credentials are refused", raw: "https://user:pass@github.com/x", wantErr: true},
		{name: "a file url is refused", raw: "file:///etc/passwd", wantErr: true},
		{name: "loopback is refused", raw: "https://127.0.0.1/x", wantErr: true},
		{name: "cloud metadata is refused", raw: "https://169.254.169.254/latest/meta-data/", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			parsed := mustParseURL(t, tt.raw)
			err := requireArtifactURL(parsed)
			if tt.wantErr != (err != nil) {
				t.Fatalf("requireArtifactURL(%q) error = %v, wantErr %v", tt.raw, err, tt.wantErr)
			}
		})
	}
}

func TestCheckArtifactRedirect_BoundsTheChain(t *testing.T) {
	t.Parallel()

	request := &http.Request{URL: mustParseURL(t, "https://objects.githubusercontent.com/object")}

	if err := checkArtifactRedirect(request, make([]*http.Request, maxRedirects-1)); err != nil {
		t.Fatalf("checkArtifactRedirect() error = %v, want a short chain to be allowed", err)
	}
	if err := checkArtifactRedirect(request, make([]*http.Request, maxRedirects)); err == nil {
		t.Fatal("checkArtifactRedirect() allowed a chain past the bound")
	}
}

func assertNoPartialFiles(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), stagingSuffix) {
			t.Fatalf("%s was left behind in %s", entry.Name(), dir)
		}
	}
}
