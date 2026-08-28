package upgrade

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		version  string
		platform Platform
		want     string
		wantErr  bool
	}{
		{name: "linux amd64", version: "v0.1.4", platform: Platform{OS: "linux", Arch: "amd64"}, want: "oberwatch_0.1.4_linux_amd64.tar.gz"},
		{name: "linux arm64", version: "v1.10.0", platform: Platform{OS: "linux", Arch: "arm64"}, want: "oberwatch_1.10.0_linux_arm64.tar.gz"},

		{name: "prerelease has no published archive", version: "v0.1.4-rc.1", platform: Platform{OS: "linux", Arch: "amd64"}, wantErr: true},
		{name: "darwin has no published archive", version: "v0.1.4", platform: Platform{OS: "darwin", Arch: "arm64"}, wantErr: true},
		{name: "windows has no published archive", version: "v0.1.4", platform: Platform{OS: "windows", Arch: "amd64"}, wantErr: true},
		{name: "386 has no published archive", version: "v0.1.4", platform: Platform{OS: "linux", Arch: "386"}, wantErr: true},
		{name: "empty platform", version: "v0.1.4", platform: Platform{}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ArchiveName(mustParseVersion(t, tt.version), tt.platform)
			if tt.wantErr != (err != nil) {
				t.Fatalf("ArchiveName(%s, %s) error = %v, wantErr %v", tt.version, tt.platform, err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("ArchiveName(%s, %s) = %q, want %q", tt.version, tt.platform, got, tt.want)
			}
		})
	}
}

func TestArchiveURL_StaysOnTheReleaseHostAndPath(t *testing.T) {
	t.Parallel()

	linux := Platform{OS: "linux", Arch: "amd64"}

	archive, err := ArchiveURL("", mustParseVersion(t, "v0.1.4"), linux)
	if err != nil {
		t.Fatalf("ArchiveURL() error = %v", err)
	}
	want := DownloadBaseURL + "v0.1.4/oberwatch_0.1.4_linux_amd64.tar.gz"
	if archive != want {
		t.Fatalf("ArchiveURL() = %q, want %q", archive, want)
	}

	checksums, err := ChecksumsURL("", mustParseVersion(t, "v0.1.4"))
	if err != nil {
		t.Fatalf("ChecksumsURL() error = %v", err)
	}
	if checksums != DownloadBaseURL+"v0.1.4/"+ChecksumsName {
		t.Fatalf("ChecksumsURL() = %q", checksums)
	}
}

// A version is the only thing that reaches a URL, so this pins that a version
// which somehow carried a URL-significant character can never widen the URL.
func TestReleaseAssetURL_RefusesVersionsThatAreNotASingleSegment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version Version
	}{
		{name: "slash in prerelease", version: Version{Major: 1, Prerelease: "a/b"}},
		{name: "query in prerelease", version: Version{Major: 1, Prerelease: "a?b"}},
		{name: "fragment in prerelease", version: Version{Major: 1, Prerelease: "a#b"}},
		{name: "backslash in prerelease", version: Version{Major: 1, Prerelease: `a\b`}},
		{name: "traversal in prerelease", version: Version{Major: 1, Prerelease: "../../etc"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := releaseAssetURL("", tt.version, ChecksumsName); err == nil {
				t.Fatalf("releaseAssetURL(%+v) accepted a version that is not a single URL segment", tt.version)
			}
		})
	}
}

func TestDefaultEndpointsArePinnedHTTPSReleaseHosts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
	}{
		{name: "release metadata endpoint", raw: LatestReleaseURL},
		{name: "artifact download prefix", raw: DownloadBaseURL},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			parsed, err := url.Parse(tt.raw)
			if err != nil {
				t.Fatalf("url.Parse(%q) error = %v", tt.raw, err)
			}
			if parsed.Scheme != "https" {
				t.Errorf("%s scheme = %q, want https", tt.name, parsed.Scheme)
			}
			if parsed.User != nil {
				t.Errorf("%s carries credentials", tt.name)
			}
			if !allowedArtifactHost(parsed.Hostname()) {
				t.Errorf("%s host %q is not a pinned release host", tt.name, parsed.Hostname())
			}
			if err := requireArtifactURL(parsed); err != nil {
				t.Errorf("%s is not an acceptable release URL: %v", tt.name, err)
			}
		})
	}
}

func TestStagedArchivePath_IsInsideTheStateDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path, err := StagedArchivePath(dir, mustParseVersion(t, "v0.1.4"), Platform{OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatalf("StagedArchivePath() error = %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("StagedArchivePath() = %q, want a file directly inside %q", path, dir)
	}
	if strings.Contains(path, "..") {
		t.Fatalf("StagedArchivePath() = %q, must not contain a traversal", path)
	}

	if _, err := StagedArchivePath("", mustParseVersion(t, "v0.1.4"), Platform{OS: "linux", Arch: "amd64"}); err == nil {
		t.Fatal("StagedArchivePath() accepted an empty state directory")
	}
}

func TestRequireSafeDir(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mode    os.FileMode
		wantErr bool
	}{
		{name: "owner only", mode: 0o700},
		{name: "owner write, group and world read", mode: 0o755},
		{name: "group writable is refused", mode: 0o770, wantErr: true},
		{name: "world writable is refused", mode: 0o777, wantErr: true},
		{name: "world writable without group is refused", mode: 0o707, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := filepath.Join(t.TempDir(), "state")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatalf("Mkdir() error = %v", err)
			}
			if err := os.Chmod(dir, tt.mode); err != nil {
				t.Fatalf("Chmod() error = %v", err)
			}

			err := requireSafeDir(dir)
			if tt.wantErr != (err != nil) {
				t.Fatalf("requireSafeDir(mode %o) error = %v, wantErr %v", tt.mode, err, tt.wantErr)
			}
		})
	}
}

func TestRequireSafeDir_RefusesAFileAndAMissingDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	file := filepath.Join(root, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := requireSafeDir(file); err == nil {
		t.Error("requireSafeDir() accepted a regular file as the state directory")
	}
	if err := requireSafeDir(filepath.Join(root, "missing")); err == nil {
		t.Error("requireSafeDir() accepted a missing directory")
	}
}

func TestRequireRegularFile_RefusesSymlinksAndDirectories(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("payload"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	if _, err := requireRegularFile(target); err != nil {
		t.Errorf("requireRegularFile(regular file) error = %v", err)
	}
	if _, err := requireRegularFile(link); err == nil {
		t.Error("requireRegularFile() accepted a symlink; a planted link could redirect a read or a swap")
	}
	if _, err := requireRegularFile(root); err == nil {
		t.Error("requireRegularFile() accepted a directory")
	}
}
