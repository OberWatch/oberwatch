package upgrade

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	// ErrNoUpdate means there is nothing newer to install.
	ErrNoUpdate = errors.New("no newer stable release is available")

	// ErrInProgress means an upgrade is already being prepared.
	ErrInProgress = errors.New("an upgrade is already in progress")
)

// Status is the whole answer the dashboard needs to decide what to show next to
// the version. Every field is either observed or explicitly empty; nothing is
// inferred.
//
//nolint:govet // Keep fields grouped by what they describe, not by width.
type Status struct {
	// CheckedAt is when the release check that produced LatestVersion ran. It
	// is nil until a check has completed, which is what tells the dashboard to
	// show "checking" rather than "up to date".
	CheckedAt *time.Time

	// LastResult is the outcome of the most recent apply, read back from disk
	// so it survives the restart the apply causes. It is nil when there is
	// none.
	LastResult *Result

	// CurrentVersion is the running version, as the binary reports it.
	CurrentVersion string

	// LatestVersion is the newest stable release tag, or empty when no check
	// has succeeded.
	LatestVersion string

	// CheckError describes why the last release check failed, and is empty
	// after a success.
	CheckError string

	// UnsupportedReason states why this installation cannot apply an upgrade.
	UnsupportedReason string

	// Fallback is what to do instead when Supported is false.
	Fallback string

	// UpdateAvailable is true only when a newer stable release was actually
	// observed. A failed check leaves it false without claiming to be current.
	UpdateAvailable bool

	// Supported reports whether this installation can apply an upgrade.
	Supported bool

	// InProgress is true while an upgrade is being prepared.
	InProgress bool
}

// Manager answers the dashboard's upgrade questions and prepares an upgrade for
// the privileged applier.
//
// It is the unprivileged half of the flow: it reads public release metadata,
// downloads and verifies the release archive for its own platform, and writes a
// request naming the validated version. It never replaces a binary, restarts a
// service, or touches configuration or data.
//
//nolint:govet // Keep fields grouped by dependency role and lifecycle.
type Manager struct {
	checker  *Checker
	fetcher  *Fetcher
	detector *Detector
	now      func() time.Time

	stateDir   string
	platform   Platform
	currentRaw string
	current    Version

	// environment is detected once at construction for the status view, and
	// re-detected inside Prepare so the decision that matters is made against
	// the state of the machine at that moment.
	environment Environment

	mu        sync.Mutex
	preparing bool
}

// NewManager builds a Manager for the running version. The version string is
// the one the binary reports; a version that is not a release tag leaves the
// installation unsupported with an honest reason.
func NewManager(currentVersion string) *Manager {
	return newManager(currentVersion, NewChecker(), NewFetcher(), NewDetector(), StateDir, CurrentPlatform(), nil)
}

func newManager(
	currentVersion string,
	checker *Checker,
	fetcher *Fetcher,
	detector *Detector,
	stateDir string,
	platform Platform,
	now func() time.Time,
) *Manager {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	manager := &Manager{
		checker:    checker,
		fetcher:    fetcher,
		detector:   detector,
		now:        now,
		stateDir:   stateDir,
		platform:   platform,
		currentRaw: strings.TrimSpace(currentVersion),
	}
	if parsed, err := ParseVersion(manager.currentRaw); err == nil {
		manager.current = parsed
	}
	manager.environment = detector.Detect(manager.currentRaw)
	return manager
}

// Status reports what the dashboard should show, and starts a background
// release check when the cached one has aged out. It never blocks on the
// network: a cold cache is reported as "not checked yet".
func (m *Manager) Status(ctx context.Context) Status {
	m.checker.EnsureFresh(ctx)
	snapshot := m.checker.Snapshot()

	// Read through the accessor: Prepare re-detects and replaces the environment
	// under the lock, and a status request can arrive while that is happening.
	environment := m.Environment()

	status := Status{
		CurrentVersion:    m.currentRaw,
		CheckError:        snapshot.Err,
		Supported:         environment.Supported,
		UnsupportedReason: environment.Reason,
		Fallback:          environment.Fallback,
		InProgress:        m.inProgress(),
	}
	if !snapshot.CheckedAt.IsZero() {
		checkedAt := snapshot.CheckedAt
		status.CheckedAt = &checkedAt
	}
	if snapshot.Latest != nil {
		status.LatestVersion = snapshot.Latest.Tag()
		status.UpdateAvailable = m.currentIsRelease() && IsUpgrade(m.current, *snapshot.Latest)
	}
	if result, found, err := ReadResult(m.stateDir); err == nil && found {
		status.LastResult = &result
	}

	return status
}

// Prepare downloads and verifies the release archive for the newest stable
// release and hands it to the privileged applier.
//
// Nothing about the target comes from the caller. The version is the one the
// release check returned, the archive name and URL are derived from it, and the
// request file names only that version. Prepare therefore has no parameter a
// request could influence.
//
// It returns the version that was handed off. The service is restarted onto it
// by the privileged applier shortly afterwards.
func (m *Manager) Prepare(ctx context.Context) (Version, error) {
	if !m.beginPrepare() {
		return Version{}, ErrInProgress
	}
	defer m.endPrepare()

	environment := m.detector.Detect(m.currentRaw)
	m.setEnvironment(environment)
	if !environment.Supported {
		return Version{}, fmt.Errorf("%w: %s", ErrUnsupported, environment.Reason)
	}
	if !m.currentIsRelease() {
		return Version{}, fmt.Errorf("%w: the running version %q is not a release tag", ErrUnsupported, m.currentRaw)
	}

	snapshot := m.checker.Refresh(ctx)
	if snapshot.Latest == nil {
		if snapshot.Err != "" {
			return Version{}, fmt.Errorf("%w: %s", ErrReleaseUnavailable, snapshot.Err)
		}
		return Version{}, ErrReleaseUnavailable
	}
	target := *snapshot.Latest
	if !IsUpgrade(m.current, target) {
		return Version{}, fmt.Errorf("%w: running %s, latest %s", ErrNoUpdate, m.current, target)
	}

	if err := requireSafeDir(m.stateDir); err != nil {
		return Version{}, err
	}

	// A previous outcome and a previous request are cleared before the new
	// download starts, so the dashboard cannot show a stale result as this
	// attempt's, and a stale request cannot be applied instead of this one.
	if err := RemoveResult(m.stateDir); err != nil {
		return Version{}, err
	}
	if err := RemoveRequest(m.stateDir); err != nil {
		return Version{}, err
	}

	archiveName, err := ArchiveName(target, m.platform)
	if err != nil {
		return Version{}, err
	}
	if err := pruneStagedArchives(m.stateDir, archiveName); err != nil {
		return Version{}, err
	}

	if _, err := m.fetcher.DownloadArchive(ctx, target, m.platform, m.stateDir); err != nil {
		return Version{}, err
	}

	request := Request{
		Tag:         target.Tag(),
		From:        m.currentRaw,
		RequestedAt: m.now().UTC().Format(time.RFC3339),
	}
	if err := WriteRequest(m.stateDir, request); err != nil {
		return Version{}, err
	}

	return target, nil
}

// CurrentVersion returns the running version as the binary reports it.
func (m *Manager) CurrentVersion() string {
	return m.currentRaw
}

// Environment reports the currently known support state.
func (m *Manager) Environment() Environment {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.environment
}

func (m *Manager) setEnvironment(environment Environment) {
	m.mu.Lock()
	m.environment = environment
	m.mu.Unlock()
}

func (m *Manager) currentIsRelease() bool {
	_, err := ParseReleaseTag(m.currentRaw)
	return err == nil
}

func (m *Manager) beginPrepare() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.preparing {
		return false
	}
	m.preparing = true
	return true
}

func (m *Manager) endPrepare() {
	m.mu.Lock()
	m.preparing = false
	m.mu.Unlock()
}

func (m *Manager) inProgress() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.preparing
}

// pruneStagedArchives removes archives left by earlier attempts, keeping the
// one named by keep.
//
// Only names this package itself produces are removed: an entry has to be a
// regular file whose name is the archive prefix followed by the tar.gz or
// partial-download suffix. Anything else in the directory is left alone, so a
// mistake here cannot turn into deleting an unrelated file.
func pruneStagedArchives(stateDir string, keep string) error {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return fmt.Errorf("read upgrade state directory: %w", err)
	}

	prefix := BinaryName + "_"
	for _, entry := range entries {
		name := entry.Name()
		if name == keep || !entry.Type().IsRegular() || !strings.HasPrefix(name, prefix) {
			continue
		}
		if !strings.HasSuffix(name, ".tar.gz") && !strings.HasSuffix(name, ".tar.gz"+stagingSuffix) {
			continue
		}
		if err := os.Remove(filepath.Join(stateDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale staged archive %s: %w", name, err)
		}
	}
	return nil
}
