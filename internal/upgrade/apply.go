package upgrade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// verifyTimeout bounds the "does the new binary run and report the version
	// we asked for" check.
	verifyTimeout = 30 * time.Second

	// restartTimeout bounds the service restart.
	restartTimeout = 60 * time.Second

	// maxVerifyOutputBytes bounds what is read from the staged binary's output.
	maxVerifyOutputBytes = 8 << 10
)

var (
	// ErrNotNewer means the request asked for a version that is not newer than
	// what is installed. It covers a downgrade and a reinstall of the same
	// version, both of which the applier refuses.
	ErrNotNewer = errors.New("requested release is not newer than the installed version")

	// ErrRefused means the applier declined to install. Nothing was replaced.
	ErrRefused = errors.New("upgrade refused")

	// errNoSystemctl means the service manager could not be found.
	errNoSystemctl = errors.New("systemctl was not found")

	// serviceNamePattern bounds the unit name that may be restarted. The name
	// is a package constant, so this is a guard against a future change rather
	// than a filter on input.
	serviceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

	// systemctlPaths are the absolute locations systemctl is looked for at. PATH
	// is deliberately not consulted: this runs as root, and resolving a program
	// name through an inherited PATH is how a root process ends up running
	// something else.
	systemctlPaths = []string{
		"/usr/bin/systemctl",
		"/bin/systemctl",
		"/usr/sbin/systemctl",
		"/sbin/systemctl",
	}
)

// Applier is the privileged half of the upgrade flow.
//
// It re-establishes every fact for itself rather than trusting the request:
// it re-parses the requested version, refuses anything that is not strictly
// newer than the version it is itself built as, fetches the release checksums
// from the pinned release host, verifies the staged archive against them, and
// only then extracts and swaps the binary. A compromised unprivileged service
// can therefore cause at most the installation of a genuine, newer, published
// release — never a binary of its choosing.
//
// Configuration and data are never touched: the only paths written are the
// install path, a backup next to it, and the handoff directory.
//
//nolint:govet // Keep fields grouped by dependency role, not by width.
type Applier struct {
	// Fetcher downloads the release checksums. It is the applier's own,
	// independent of whatever the service used.
	Fetcher *Fetcher

	// Restart restarts the service after a successful swap. A nil value uses
	// systemctl.
	Restart func(ctx context.Context) error

	// VerifyBinary checks that the extracted binary runs and reports the
	// version that was requested. A nil value runs "<binary> version".
	VerifyBinary func(ctx context.Context, binaryPath string, target Version) error

	// Now is the clock used for the recorded finish time.
	Now func() time.Time

	// StateDir is the handoff directory.
	StateDir string

	// InstallPath is the binary to replace. Empty means "the running
	// executable", which is the correct answer for the installed applier: it is
	// the installed binary.
	InstallPath string

	// Installed is the version this applier is built as, which is the version
	// currently installed.
	Installed Version

	// Platform is the release platform, used to derive the staged archive name.
	Platform Platform
}

// NewApplier builds an Applier for the installed version.
func NewApplier(installed Version) *Applier {
	return &Applier{
		Fetcher:   NewFetcher(),
		StateDir:  StateDir,
		Installed: installed,
		Platform:  CurrentPlatform(),
	}
}

// Apply installs the waiting upgrade request.
//
// It returns ErrNoRequest when there is nothing to do, which is the normal
// state: the applier is started whenever the request file appears and must be
// safe to run at any other time too.
//
// Every outcome, including a refusal, is recorded in the handoff directory
// before Apply returns, so the dashboard can report what happened after the
// restart.
func (a *Applier) Apply(ctx context.Context) (Result, error) {
	if err := requireSafeDir(a.StateDir); err != nil {
		return Result{}, err
	}

	request, target, err := ReadRequest(a.StateDir)
	if errors.Is(err, ErrNoRequest) {
		return Result{}, ErrNoRequest
	}
	if err != nil {
		// The request is removed even though it did not validate: leaving it in
		// place would re-trigger the applier on every path-unit activation.
		_ = RemoveRequest(a.StateDir)
		return a.refuse(a.Installed, "", fmt.Sprintf("refused an upgrade request that did not validate: %v", err))
	}

	// The request is consumed before any work starts. The privileged applier is
	// started by this file appearing, so a request that survived a failure would
	// be retried forever.
	if removeErr := RemoveRequest(a.StateDir); removeErr != nil {
		return a.refuse(a.Installed, request.From, removeErr.Error())
	}

	if Compare(target, a.Installed) <= 0 {
		return a.refuse(target, request.From, fmt.Sprintf("%v: requested %s, installed %s", ErrNotNewer, target, a.Installed))
	}

	installPath, err := a.resolveInstallPath()
	if err != nil {
		return a.refuse(target, request.From, err.Error())
	}

	archiveName, expectedDigest, err := a.publishedDigest(ctx, target)
	if err != nil {
		return a.refuse(target, request.From, err.Error())
	}

	archive, err := a.openStagedArchive(target)
	if err != nil {
		return a.refuse(target, request.From, err.Error())
	}
	archivePath := archive.Name()
	defer func() { _ = archive.Close() }()

	binaryPath, err := a.extractVerifiedBinary(ctx, archive, archiveName, expectedDigest, installPath, target)
	if err != nil {
		return a.refuse(target, request.From, err.Error())
	}
	defer func() { _ = os.Remove(binaryPath) }()

	backupPath := filepath.Join(filepath.Dir(installPath), BackupName)
	if err := copyFile(installPath, backupPath, 0o755); err != nil {
		return a.refuse(target, request.From, fmt.Sprintf("keep a rollback copy of %s: %v", installPath, err))
	}

	// The swap is a rename inside one directory, so the install path is either
	// the old binary or the new one and never a partially written file.
	if err := os.Rename(binaryPath, installPath); err != nil {
		return a.refuse(target, request.From, fmt.Sprintf("replace %s: %v", installPath, err))
	}
	_ = os.Remove(archivePath)

	rollback := fmt.Sprintf("To roll back: sudo install -m 0755 %s %s && sudo systemctl restart %s", backupPath, installPath, ServiceName)
	result := Result{
		Status:     ResultSucceeded,
		Tag:        target.Tag(),
		From:       request.From,
		Message:    fmt.Sprintf("Installed %s and restarted %s. Configuration and data were not changed. %s", target, ServiceName, rollback),
		FinishedAt: a.finishedAt(),
	}

	if err := a.restart(ctx); err != nil {
		result.Status = ResultRestartRequired
		result.Message = fmt.Sprintf(
			"Installed %s, but restarting %s failed: %v. The previous version is still running. Restart it with: sudo systemctl restart %s. %s",
			target, ServiceName, err, ServiceName, rollback,
		)
	}

	if err := WriteResult(a.StateDir, result); err != nil {
		return result, err
	}
	return result, nil
}

// refuse records a refusal and returns it. Nothing has been replaced when this
// is reached.
func (a *Applier) refuse(target Version, from string, message string) (Result, error) {
	result := Result{
		Status:     ResultFailed,
		Tag:        target.Tag(),
		From:       from,
		Message:    fmt.Sprintf("%s. Nothing was installed and the running version was not changed.", message),
		FinishedAt: a.finishedAt(),
	}
	if err := WriteResult(a.StateDir, result); err != nil {
		return result, err
	}
	return result, fmt.Errorf("%w: %s", ErrRefused, message)
}

// resolveInstallPath returns the binary to replace, refusing an install
// directory that anyone other than its owner can write to.
func (a *Applier) resolveInstallPath() (string, error) {
	path := a.InstallPath
	if strings.TrimSpace(path) == "" {
		executable, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("locate the running executable: %w", err)
		}
		path = executable
	}

	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve install path %s: %w", path, err)
	}
	if _, err := requireRegularFile(resolved); err != nil {
		return "", fmt.Errorf("install path: %w", err)
	}
	if err := requireSafeDir(filepath.Dir(resolved)); err != nil {
		return "", fmt.Errorf("install directory: %w", err)
	}
	return resolved, nil
}

// publishedDigest fetches the release checksums itself and returns the archive
// name and the SHA-256 the release publishes for it.
//
// This is the root of trust for the privileged half. The service that staged the
// archive is unprivileged and network-facing; its word about what it downloaded
// is not accepted, and the digest to compare against is not read from anything
// the service can write.
func (a *Applier) publishedDigest(ctx context.Context, target Version) (string, string, error) {
	archiveName, err := ArchiveName(target, a.Platform)
	if err != nil {
		return "", "", err
	}

	sums, err := a.fetcher().Checksums(ctx, target)
	if err != nil {
		return "", "", fmt.Errorf("read release checksums: %w", err)
	}
	expected, ok := sums[archiveName]
	if !ok {
		return "", "", fmt.Errorf("%w: %s", ErrChecksumMissing, archiveName)
	}
	return archiveName, expected, nil
}

// openStagedArchive opens the archive the service staged, at the path both
// halves derive from the same validated version.
func (a *Applier) openStagedArchive(target Version) (*os.File, error) {
	archivePath, err := StagedArchivePath(a.StateDir, target, a.Platform)
	if err != nil {
		return nil, err
	}
	archive, err := OpenStagedArchive(archivePath)
	if err != nil {
		return nil, fmt.Errorf("read staged archive: %w", err)
	}
	return archive, nil
}

// extractVerifiedBinary unpacks the binary next to the install path, in a single
// pass that also hashes the archive, and checks that the result runs and reports
// the version that was requested.
//
// Hashing and extracting in one pass over one open handle is what makes the
// verification meaningful. The handoff directory belongs to the unprivileged
// service, so hashing the archive and then reading it again — even through the
// same handle — would leave a window in which the service could rewrite the file
// and have root install bytes that were never checked. Here the bytes that reach
// the hasher are exactly the bytes that were extracted, and nothing is installed
// until the digest matches.
func (a *Applier) extractVerifiedBinary(
	ctx context.Context,
	archive io.Reader,
	archiveName string,
	expectedDigest string,
	installPath string,
	target Version,
) (string, error) {
	binaryPath := filepath.Join(filepath.Dir(installPath), applyTempPrefix+target.Tag())

	// A leftover from an interrupted attempt is removed rather than reused, so
	// the extraction below always writes a fresh file.
	if err := os.Remove(binaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("clear %s: %w", binaryPath, err)
	}

	hasher := sha256.New()
	hashed := io.TeeReader(archive, hasher)

	if err := ExtractBinary(hashed, binaryPath); err != nil {
		return "", err
	}

	// Extraction stops at the member it wanted, so the rest of the archive is
	// read to make the digest cover every byte of the file.
	if _, err := io.Copy(io.Discard, io.LimitReader(hashed, MaxArchiveBytes)); err != nil {
		_ = os.Remove(binaryPath)
		return "", fmt.Errorf("read staged archive: %w", err)
	}

	if err := VerifyDigest(expectedDigest, hex.EncodeToString(hasher.Sum(nil))); err != nil {
		_ = os.Remove(binaryPath)
		return "", fmt.Errorf("%s: %w", archiveName, err)
	}

	if err := a.verifyBinary(ctx, binaryPath, target); err != nil {
		_ = os.Remove(binaryPath)
		return "", err
	}
	return binaryPath, nil
}

func (a *Applier) verifyBinary(ctx context.Context, binaryPath string, target Version) error {
	if a.VerifyBinary != nil {
		return a.VerifyBinary(ctx, binaryPath, target)
	}
	return verifyBinaryVersion(ctx, binaryPath, target)
}

// verifyBinaryVersion runs "<binary> version" and requires the requested tag in
// its output. The binary has already been verified against the release
// checksums, so this is a check that the right release was staged, not a
// sandbox.
func verifyBinaryVersion(ctx context.Context, binaryPath string, target Version) error {
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()

	if !filepath.IsAbs(binaryPath) {
		return fmt.Errorf("staged binary path %q is not absolute", binaryPath)
	}

	var output bytes.Buffer
	command := exec.CommandContext(ctx, binaryPath, "version")
	command.Stdout = &limitedWriter{writer: &output, remaining: maxVerifyOutputBytes}
	command.Stderr = io.Discard
	command.Env = []string{}
	if err := command.Run(); err != nil {
		return fmt.Errorf("run %s version: %w", binaryPath, err)
	}
	if !strings.Contains(output.String(), target.Tag()) {
		return fmt.Errorf("staged binary does not report %s", target)
	}
	return nil
}

func (a *Applier) restart(ctx context.Context) error {
	if a.Restart != nil {
		return a.Restart(ctx)
	}
	return RestartService(ctx, ServiceName)
}

// RestartService restarts a systemd unit by name.
//
// systemctl is located at an absolute path rather than through PATH, and the
// unit name is checked against a narrow pattern. No shell is involved, so there
// is nothing for a name to be injected into even if one ever came from
// somewhere other than a constant.
func RestartService(ctx context.Context, service string) error {
	if !serviceNamePattern.MatchString(service) {
		return fmt.Errorf("service name %q is not a plain unit name", service)
	}

	binary, err := findSystemctl(systemctlPaths)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, restartTimeout)
	defer cancel()

	command := exec.CommandContext(ctx, binary, "restart", service)
	command.Env = []string{}
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("%s restart %s: %w: %s", binary, service, err, sanitizeMessage(string(output)))
	}
	return nil
}

// findSystemctl returns the first candidate that is an executable regular file.
// Candidates are absolute paths, never names resolved through PATH.
func findSystemctl(candidates []string) (string, error) {
	for _, candidate := range candidates {
		if !filepath.IsAbs(candidate) {
			continue
		}
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		return candidate, nil
	}
	return "", fmt.Errorf("%w in %s", errNoSystemctl, strings.Join(candidates, ", "))
}

func (a *Applier) fetcher() *Fetcher {
	if a.Fetcher == nil {
		return NewFetcher()
	}
	return a.Fetcher
}

func (a *Applier) finishedAt() string {
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	return now().UTC().Format(time.RFC3339)
}

// copyFile copies a regular file, replacing the destination. It is used to keep
// a rollback copy of the binary being replaced, so the copy is made before the
// swap and the live path stays valid throughout.
func copyFile(sourcePath string, destinationPath string, mode os.FileMode) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open %s: %w", sourcePath, err)
	}
	defer func() { _ = source.Close() }()

	temporaryPath := destinationPath + stagingSuffix
	destination, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", temporaryPath, err)
	}
	defer func() { _ = destination.Close() }()

	if _, err := io.Copy(destination, io.LimitReader(source, MaxBinaryBytes)); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("copy %s to %s: %w", sourcePath, temporaryPath, err)
	}
	if err := destination.Chmod(mode); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("chmod %s: %w", temporaryPath, err)
	}
	if err := destination.Sync(); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("flush %s: %w", temporaryPath, err)
	}
	if err := destination.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("close %s: %w", temporaryPath, err)
	}
	if err := os.Rename(temporaryPath, destinationPath); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("rename %s to %s: %w", temporaryPath, destinationPath, err)
	}
	return nil
}

// limitedWriter bounds how much of a subprocess's output is kept.
type limitedWriter struct {
	writer    io.Writer
	remaining int
}

// Write implements io.Writer, discarding everything past the bound.
//
// It reports the whole slice as written even when part of it was dropped: the
// caller is a subprocess pipe, and reporting a short write would turn a bounded
// read into an error.
func (l *limitedWriter) Write(data []byte) (int, error) {
	if l.remaining <= 0 {
		return len(data), nil
	}
	kept := data
	if len(kept) > l.remaining {
		kept = kept[:l.remaining]
	}
	written, err := l.writer.Write(kept)
	l.remaining -= written
	if err != nil {
		return written, err
	}
	return len(data), nil
}
