package upgrade

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Everything the upgrade flow reads, writes or fetches is derived from the
// constants below plus a strictly parsed Version. No request body, query
// parameter, header, config value or environment variable ever contributes a
// URL, tag, path or command. That is deliberate: it is the property that makes
// "arbitrary command, URL, tag or script execution" impossible rather than
// merely filtered.
const (
	// LatestReleaseURL is the public release metadata endpoint. It is read
	// without credentials.
	LatestReleaseURL = "https://api.github.com/repos/OberWatch/oberwatch/releases/latest"

	// DownloadBaseURL is the only prefix release artifacts are fetched from.
	// A tag is appended to it, never substituted into it.
	DownloadBaseURL = "https://github.com/OberWatch/oberwatch/releases/download/"

	// ChecksumsName is the release asset that lists the SHA-256 of every
	// archive in the release.
	ChecksumsName = "checksums.txt"

	// BinaryName is the only archive entry the applier will extract.
	BinaryName = "oberwatch"

	// StateDir is the handoff directory the installer provisions: writable by
	// the service user, read by the privileged applier. Its presence is part of
	// how a supported installation is recognised.
	StateDir = "/var/lib/oberwatch/upgrade"

	// RequestName is the file the server writes to ask for an upgrade.
	RequestName = "request.json"

	// ResultName is the file the privileged applier writes when it finishes.
	// The server reads it after the restart to report an honest outcome.
	ResultName = "result.json"

	// ApplyUnitPath is the privileged applier unit. The server checks that it
	// exists before offering an in-dashboard upgrade, so the button is never
	// shown on an installation where nothing can apply the request.
	ApplyUnitPath = "/etc/systemd/system/oberwatch-upgrade.service"

	// ServiceName is the unit restarted after a successful swap.
	ServiceName = "oberwatch"

	// BackupName is the file name the replaced binary is kept under, next to
	// the install path, for rollback.
	BackupName = "oberwatch.previous"

	// stagingSuffix marks a partially downloaded archive. A staged archive is
	// only renamed to its final name after its checksum is verified, so the
	// applier never sees an unverified download under the name it looks for.
	stagingSuffix = ".part"

	// applyTempPrefix names the extracted binary before it is swapped in. It is
	// created next to the install path so the swap is a rename within one
	// directory, which is atomic.
	applyTempPrefix = ".oberwatch-upgrade-"
)

// ErrUnsupportedPlatform is returned for an OS or architecture that has no
// release archive.
var ErrUnsupportedPlatform = errors.New("unsupported platform")

// Platform is the OS and architecture a release archive is built for.
type Platform struct {
	OS   string
	Arch string
}

// CurrentPlatform reports the platform this binary was built for.
func CurrentPlatform() Platform {
	return Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
}

// Supported reports whether releases carry an archive for this platform. The
// release build matrix is Linux amd64 and arm64; anything else has no artifact
// to install and must be told so rather than offered a button.
func (p Platform) Supported() bool {
	if p.OS != "linux" {
		return false
	}
	return p.Arch == "amd64" || p.Arch == "arm64"
}

// String returns "os/arch".
func (p Platform) String() string {
	return p.OS + "/" + p.Arch
}

// ArchiveName returns the release archive file name for a stable version on
// the given platform. It refuses prereleases and unsupported platforms so a
// name is never built from a version whose artifacts do not exist.
func ArchiveName(version Version, platform Platform) (string, error) {
	if !version.IsStable() {
		return "", fmt.Errorf("%w: %s is not a stable release", ErrInvalidVersion, version)
	}
	if !platform.Supported() {
		return "", fmt.Errorf("%w: %s", ErrUnsupportedPlatform, platform)
	}
	return fmt.Sprintf("%s_%s_%s_%s.tar.gz", BinaryName, version.Core(), platform.OS, platform.Arch), nil
}

// ArchiveURL returns the download URL for a release archive.
func ArchiveURL(baseURL string, version Version, platform Platform) (string, error) {
	name, err := ArchiveName(version, platform)
	if err != nil {
		return "", err
	}
	return releaseAssetURL(baseURL, version, name)
}

// ChecksumsURL returns the download URL for a release's checksums document.
func ChecksumsURL(baseURL string, version Version) (string, error) {
	if !version.IsStable() {
		return "", fmt.Errorf("%w: %s is not a stable release", ErrInvalidVersion, version)
	}
	return releaseAssetURL(baseURL, version, ChecksumsName)
}

// releaseAssetURL joins the fixed download prefix, a validated tag and a fixed
// asset name. The tag is re-parsed here rather than trusted from the caller, so
// no path segment can be smuggled in through it.
func releaseAssetURL(baseURL string, version Version, assetName string) (string, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DownloadBaseURL
	}
	tag := version.Tag()
	if _, err := ParseReleaseTag(tag); err != nil {
		return "", err
	}
	if strings.ContainsAny(tag, "/?#\\") || strings.ContainsAny(assetName, "/?#\\") {
		return "", fmt.Errorf("%w: %q or %q is not a single URL segment", ErrInvalidVersion, tag, assetName)
	}
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}
	return baseURL + tag + "/" + assetName, nil
}

// StagedArchivePath returns where a verified archive for a version waits for
// the privileged applier. Both sides derive it from the same validated version,
// so the applier never opens a path named by the request file.
func StagedArchivePath(stateDir string, version Version, platform Platform) (string, error) {
	name, err := ArchiveName(version, platform)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(stateDir) == "" {
		return "", errors.New("upgrade state directory is empty")
	}
	return filepath.Join(stateDir, name), nil
}

// RequestPath returns the handoff request file path inside a state directory.
func RequestPath(stateDir string) string {
	return filepath.Join(stateDir, RequestName)
}

// ResultPath returns the handoff result file path inside a state directory.
func ResultPath(stateDir string) string {
	return filepath.Join(stateDir, ResultName)
}

// requireSafeDir refuses a handoff directory that anyone outside its owner can
// write to. A group- or world-writable directory would let a local user stage
// an archive or a request for the privileged applier to act on, so both sides
// check it before they trust anything inside.
func requireSafeDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect upgrade state directory %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("upgrade state path %s is not a directory", dir)
	}
	if info.Mode()&0o022 != 0 {
		return fmt.Errorf("upgrade state directory %s is group- or world-writable (mode %o)", dir, info.Mode().Perm())
	}
	return nil
}

// requireRegularFile refuses anything that is not a plain file, including a
// symlink. The applier uses it before reading a staged archive or replacing the
// install path, so a symlink planted in the handoff directory cannot redirect
// either operation.
func requireRegularFile(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file (mode %s)", path, info.Mode())
	}
	return info, nil
}
