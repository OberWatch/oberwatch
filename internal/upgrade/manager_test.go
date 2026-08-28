package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// testPlatform pins the release platform so the tests behave the same wherever
// they run.
var testPlatform = Platform{OS: "linux", Arch: "amd64"}

// managerFixture is a Manager wired to a local release server and a temporary
// installation tree. No test touches the network or a real install path.
//
//nolint:govet // Keep fixture fields grouped by what they stand in for.
type managerFixture struct {
	manager  *Manager
	detector *Detector
	release  *releaseServer
	source   *fakeSource
	clock    *testClock
	stateDir string
}

// releaseAssets builds the assets a genuine release publishes for a tag.
func releaseAssets(t *testing.T, tag string) map[string]map[string][]byte {
	t.Helper()

	version := mustParseVersion(t, tag)
	archiveName, err := ArchiveName(version, testPlatform)
	if err != nil {
		t.Fatalf("ArchiveName() error = %v", err)
	}
	archive := tarGzBytes(t, releaseEntries(tag))

	return map[string]map[string][]byte{
		version.Tag(): {
			archiveName:   archive,
			ChecksumsName: checksumsDocument(map[string][]byte{archiveName: archive}),
		},
	}
}

// newManagerFixture builds a supported installation running currentVersion,
// with latestTag published. Passing an empty latestTag leaves the release check
// failing.
func newManagerFixture(t *testing.T, currentVersion string, latestTag string, assets map[string]map[string][]byte) *managerFixture {
	t.Helper()

	detector := supportedDetector(t)
	if assets == nil && latestTag != "" {
		assets = releaseAssets(t, latestTag)
	}
	release := newReleaseServer(t, assets)

	source := &fakeSource{}
	if latestTag == "" {
		source.err = errors.New("release source is unreachable")
	} else {
		source.latest = mustParseVersion(t, latestTag)
	}

	clock := newTestClock()
	fixture := &managerFixture{
		detector: detector,
		release:  release,
		source:   source,
		clock:    clock,
		stateDir: detector.StateDir,
	}
	fixture.manager = newManager(
		currentVersion,
		newChecker(source, clock.Now),
		&Fetcher{BaseURL: release.BaseURL, HTTPClient: newArtifactClient()},
		detector,
		detector.StateDir,
		testPlatform,
		clock.Now,
	)
	return fixture
}

func TestManager_Status_ReportsCheckingBeforeAnyCheckCompletes(t *testing.T) {
	t.Parallel()

	fixture := newManagerFixture(t, "v0.1.3", "v0.1.4", nil)
	fixture.source.block = make(chan struct{})
	defer close(fixture.source.block)

	status := fixture.manager.Status(context.Background())
	if status.CheckedAt != nil {
		t.Fatalf("Status().CheckedAt = %v, want nil before a check completes", status.CheckedAt)
	}
	if status.UpdateAvailable {
		t.Fatal("Status() claimed an update before any check completed")
	}
	if status.LatestVersion != "" {
		t.Fatalf("Status().LatestVersion = %q, want empty before a check completes", status.LatestVersion)
	}
	if status.CurrentVersion != "v0.1.3" {
		t.Fatalf("Status().CurrentVersion = %q, want v0.1.3", status.CurrentVersion)
	}
}

func TestManager_Status_VersionPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		current             string
		latest              string
		wantUpdateAvailable bool
	}{
		{name: "a newer stable release is offered", current: "v0.1.3", latest: "v0.1.4", wantUpdateAvailable: true},
		{name: "a newer minor is offered", current: "v0.1.3", latest: "v0.2.0", wantUpdateAvailable: true},
		{name: "a double digit patch is offered", current: "v0.1.9", latest: "v0.1.10", wantUpdateAvailable: true},
		{name: "the same version is not offered", current: "v0.1.4", latest: "v0.1.4"},
		{name: "an older release is not offered", current: "v0.1.4", latest: "v0.1.3"},
		{name: "a prerelease is not offered", current: "v0.1.3", latest: "v0.1.4-rc.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fixture := newManagerFixture(t, tt.current, tt.latest, map[string]map[string][]byte{})
			fixture.manager.checker.Refresh(context.Background())

			status := fixture.manager.Status(context.Background())
			if status.UpdateAvailable != tt.wantUpdateAvailable {
				t.Fatalf("Status().UpdateAvailable = %v, want %v (running %s, latest %s)", status.UpdateAvailable, tt.wantUpdateAvailable, tt.current, tt.latest)
			}
			if status.CheckedAt == nil {
				t.Fatal("Status().CheckedAt = nil after a completed check")
			}
			if status.LatestVersion != mustParseVersion(t, tt.latest).Tag() {
				t.Fatalf("Status().LatestVersion = %q, want %q", status.LatestVersion, tt.latest)
			}
			if status.CheckError != "" {
				t.Fatalf("Status().CheckError = %q, want empty after a successful check", status.CheckError)
			}
		})
	}
}

func TestManager_Status_ReportsAFailedCheckWithoutClaimingToBeUpToDate(t *testing.T) {
	t.Parallel()

	fixture := newManagerFixture(t, "v0.1.3", "", nil)
	fixture.manager.checker.Refresh(context.Background())

	status := fixture.manager.Status(context.Background())
	if status.CheckError == "" {
		t.Fatal("Status().CheckError is empty after a failed check")
	}
	if status.UpdateAvailable {
		t.Fatal("Status() claimed an update after a failed check")
	}
	if status.LatestVersion != "" {
		t.Fatalf("Status().LatestVersion = %q, want empty after a failed check", status.LatestVersion)
	}
	if status.CheckedAt != nil {
		t.Fatal("Status().CheckedAt is set after a failed check; nothing was successfully checked")
	}
}

func TestManager_Status_UnsupportedInstallationCarriesAFallback(t *testing.T) {
	t.Parallel()

	fixture := newManagerFixture(t, "v0.1.3", "v0.1.4", map[string]map[string][]byte{})
	if err := os.Remove(fixture.detector.ApplyUnitPath); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	// The environment is detected at construction, so rebuild to pick up the
	// change the same way a restarted service would.
	fixture.manager = newManager("v0.1.3", fixture.manager.checker, fixture.manager.fetcher, fixture.detector, fixture.stateDir, testPlatform, fixture.clock.Now)
	fixture.manager.checker.Refresh(context.Background())

	status := fixture.manager.Status(context.Background())
	if status.Supported {
		t.Fatal("Status().Supported is true with no privileged applier installed")
	}
	if status.UnsupportedReason == "" || status.Fallback != InstallerFallback {
		t.Fatalf("Status() = %+v, want an honest reason and the installer fallback", status)
	}
	// The newer release is still reported: an operator who cannot use the button
	// still needs to know an update exists.
	if !status.UpdateAvailable || status.LatestVersion != "v0.1.4" {
		t.Fatalf("Status() = %+v, want the available release still reported", status)
	}
}

func TestManager_Status_DevBuildIsUnsupportedAndNeverOffersAnUpgrade(t *testing.T) {
	t.Parallel()

	fixture := newManagerFixture(t, "dev", "v0.1.4", map[string]map[string][]byte{})
	fixture.manager.checker.Refresh(context.Background())

	status := fixture.manager.Status(context.Background())
	if status.Supported {
		t.Fatal("Status().Supported is true for a build that did not come from a release")
	}
	if status.UpdateAvailable {
		t.Fatal("Status() offered an upgrade from a version it cannot compare")
	}
	if status.Fallback != SourceFallback {
		t.Fatalf("Status().Fallback = %q, want the source fallback", status.Fallback)
	}
}

func TestManager_Status_ReportsTheRecordedResultAfterARestart(t *testing.T) {
	t.Parallel()

	fixture := newManagerFixture(t, "v0.1.4", "v0.1.4", map[string]map[string][]byte{})
	want := Result{
		Status:     ResultSucceeded,
		Tag:        "v0.1.4",
		From:       "v0.1.3",
		Message:    "Installed v0.1.4 and restarted oberwatch.",
		FinishedAt: "2026-08-28T12:00:05Z",
	}
	if err := WriteResult(fixture.stateDir, want); err != nil {
		t.Fatalf("WriteResult() error = %v", err)
	}

	status := fixture.manager.Status(context.Background())
	if status.LastResult == nil {
		t.Fatal("Status().LastResult = nil, want the outcome the applier recorded")
	}
	if *status.LastResult != want {
		t.Fatalf("Status().LastResult = %+v, want %+v", *status.LastResult, want)
	}
}

func TestManager_Prepare_StagesAVerifiedArchiveAndWritesTheRequest(t *testing.T) {
	t.Parallel()

	fixture := newManagerFixture(t, "v0.1.3", "v0.1.4", nil)

	target, err := fixture.manager.Prepare(context.Background())
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if target.Tag() != "v0.1.4" {
		t.Fatalf("Prepare() = %s, want v0.1.4", target)
	}

	archiveName, err := ArchiveName(target, testPlatform)
	if err != nil {
		t.Fatalf("ArchiveName() error = %v", err)
	}
	staged := filepath.Join(fixture.stateDir, archiveName)
	if _, statErr := os.Stat(staged); statErr != nil {
		t.Fatalf("the verified archive was not staged: %v", statErr)
	}

	request, version, err := ReadRequest(fixture.stateDir)
	if err != nil {
		t.Fatalf("ReadRequest() error = %v", err)
	}
	if version.Tag() != "v0.1.4" || request.Tag != "v0.1.4" {
		t.Fatalf("request = %+v, want it to name v0.1.4", request)
	}
	if request.From != "v0.1.3" {
		t.Fatalf("request.From = %q, want v0.1.3", request.From)
	}
	if _, err := time.Parse(time.RFC3339, request.RequestedAt); err != nil {
		t.Fatalf("request.RequestedAt = %q, want RFC3339: %v", request.RequestedAt, err)
	}

	assertNoPartialFiles(t, fixture.stateDir)
}

// The whole flow has no parameter for a target, so the only thing that can
// decide what is staged is the release check. This pins that: the release server
// holds assets for a version nobody checked, and they are never requested.
func TestManager_Prepare_OnlyEverFetchesTheCheckedRelease(t *testing.T) {
	t.Parallel()

	assets := releaseAssets(t, "v0.1.4")
	for tag, files := range releaseAssets(t, "v9.9.9") {
		assets[tag] = files
	}

	fixture := newManagerFixture(t, "v0.1.3", "v0.1.4", assets)
	if _, err := fixture.manager.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	for _, path := range fixture.release.RequestedPaths() {
		if !strings.HasPrefix(path, "v0.1.4/") {
			t.Fatalf("Prepare() fetched %q, want only assets of the checked release v0.1.4", path)
		}
	}

	entries, err := os.ReadDir(fixture.stateDir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "9.9.9") {
			t.Fatalf("Prepare() staged %q, which no release check ever returned", entry.Name())
		}
	}
}

func TestManager_Prepare_Refusals(t *testing.T) {
	t.Parallel()

	//nolint:govet // Keep table fields grouped by setup, then expectation.
	tests := []struct {
		name    string
		current string
		latest  string
		mutate  func(t *testing.T, fixture *managerFixture)
		wantErr error
	}{
		{
			name:    "nothing newer is published",
			current: "v0.1.4",
			latest:  "v0.1.4",
			wantErr: ErrNoUpdate,
		},
		{
			name:    "the published release is older",
			current: "v0.2.0",
			latest:  "v0.1.4",
			wantErr: ErrNoUpdate,
		},
		{
			name:    "the release check is failing",
			current: "v0.1.3",
			latest:  "",
			wantErr: ErrReleaseUnavailable,
		},
		{
			name:    "the privileged applier is not installed",
			current: "v0.1.3",
			latest:  "v0.1.4",
			mutate: func(t *testing.T, fixture *managerFixture) {
				if err := os.Remove(fixture.detector.ApplyUnitPath); err != nil {
					t.Fatalf("Remove() error = %v", err)
				}
			},
			wantErr: ErrUnsupported,
		},
		{
			name:    "the handoff directory is gone",
			current: "v0.1.3",
			latest:  "v0.1.4",
			mutate: func(t *testing.T, fixture *managerFixture) {
				if err := os.Remove(fixture.detector.StateDir); err != nil {
					t.Fatalf("Remove() error = %v", err)
				}
			},
			wantErr: ErrUnsupported,
		},
		{
			name:    "the handoff directory is world-writable",
			current: "v0.1.3",
			latest:  "v0.1.4",
			mutate: func(t *testing.T, fixture *managerFixture) {
				if err := os.Chmod(fixture.detector.StateDir, 0o777); err != nil {
					t.Fatalf("Chmod() error = %v", err)
				}
			},
			wantErr: ErrUnsupported,
		},
		{
			name:    "running in a container",
			current: "v0.1.3",
			latest:  "v0.1.4",
			mutate: func(t *testing.T, fixture *managerFixture) {
				marker := filepath.Join(t.TempDir(), ".dockerenv")
				if err := os.WriteFile(marker, nil, 0o644); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
				fixture.detector.ContainerMarkers = []string{marker}
			},
			wantErr: ErrUnsupported,
		},
		{
			name:    "the running version is not a release",
			current: "dev",
			latest:  "v0.1.4",
			wantErr: ErrUnsupported,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fixture := newManagerFixture(t, tt.current, tt.latest, nil)
			if tt.mutate != nil {
				tt.mutate(t, fixture)
			}

			_, err := fixture.manager.Prepare(context.Background())
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Prepare() error = %v, want %v", err, tt.wantErr)
			}

			if _, statErr := os.Stat(RequestPath(fixture.stateDir)); statErr == nil {
				t.Fatal("Prepare() wrote an upgrade request after refusing")
			}
		})
	}
}

func TestManager_Prepare_RefusesATamperedArchiveAndAsksForNothing(t *testing.T) {
	t.Parallel()

	archiveName, err := ArchiveName(mustParseVersion(t, "v0.1.4"), testPlatform)
	if err != nil {
		t.Fatalf("ArchiveName() error = %v", err)
	}
	genuine := tarGzBytes(t, releaseEntries("v0.1.4"))
	tampered := tarGzBytes(t, []tarEntry{binaryEntry("v0.1.4"), {Name: "backdoor", Body: "attacker"}})

	fixture := newManagerFixture(t, "v0.1.3", "v0.1.4", map[string]map[string][]byte{
		"v0.1.4": {
			archiveName:   tampered,
			ChecksumsName: checksumsDocument(map[string][]byte{archiveName: genuine}),
		},
	})

	if _, err := fixture.manager.Prepare(context.Background()); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Prepare() error = %v, want ErrChecksumMismatch", err)
	}

	if _, err := os.Stat(RequestPath(fixture.stateDir)); err == nil {
		t.Fatal("Prepare() asked for an upgrade after the archive failed verification")
	}
	if _, err := os.Stat(filepath.Join(fixture.stateDir, archiveName)); err == nil {
		t.Fatal("Prepare() left the tampered archive staged")
	}
	assertNoPartialFiles(t, fixture.stateDir)
}

func TestManager_Prepare_RunsOneAttemptAtATime(t *testing.T) {
	t.Parallel()

	fixture := newManagerFixture(t, "v0.1.3", "v0.1.4", nil)
	fixture.source.started = make(chan struct{}, 1)
	fixture.source.block = make(chan struct{})

	var (
		waitGroup sync.WaitGroup
		firstErr  error
	)
	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		_, firstErr = fixture.manager.Prepare(context.Background())
	}()

	<-fixture.source.started

	if _, err := fixture.manager.Prepare(context.Background()); !errors.Is(err, ErrInProgress) {
		t.Fatalf("a concurrent Prepare() error = %v, want ErrInProgress", err)
	}

	close(fixture.source.block)
	waitGroup.Wait()

	if firstErr != nil {
		t.Fatalf("Prepare() error = %v", firstErr)
	}
}

func TestManager_Prepare_LeavesConfigurationAndDataUntouched(t *testing.T) {
	t.Parallel()

	fixture := newManagerFixture(t, "v0.1.3", "v0.1.4", nil)

	// A stand-in for the paths the installer owns: the config file, the SQLite
	// database and its sidecars. Nothing in the upgrade flow may read, write or
	// remove any of them.
	installTree := t.TempDir()
	files := map[string]string{
		"oberwatch.toml":      "[server]\nport = 8080\n",
		"data/oberwatch.db":   "sqlite database bytes",
		"data/oberwatch.db-w": "write ahead log bytes",
	}
	if err := os.Mkdir(filepath.Join(installTree, "data"), 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(installTree, name), []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", name, err)
		}
	}
	before := snapshotTree(t, installTree)

	if _, err := fixture.manager.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	assertTreeUnchanged(t, installTree, before)
}

func TestManager_Prepare_ReplacesAStaleStagedArchiveAndResult(t *testing.T) {
	t.Parallel()

	fixture := newManagerFixture(t, "v0.1.3", "v0.1.4", nil)

	stale := filepath.Join(fixture.stateDir, "oberwatch_0.1.3_linux_amd64.tar.gz")
	if err := os.WriteFile(stale, []byte("stale archive"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	partial := filepath.Join(fixture.stateDir, "oberwatch_0.1.4_linux_amd64.tar.gz"+stagingSuffix)
	if err := os.WriteFile(partial, []byte("interrupted download"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	unrelated := filepath.Join(fixture.stateDir, "operator-note.txt")
	if err := os.WriteFile(unrelated, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := WriteResult(fixture.stateDir, Result{Status: ResultFailed, Tag: "v0.1.4", Message: "an earlier attempt"}); err != nil {
		t.Fatalf("WriteResult() error = %v", err)
	}

	if _, err := fixture.manager.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	if _, err := os.Stat(stale); err == nil {
		t.Error("Prepare() left a stale staged archive behind")
	}
	if _, err := os.Stat(partial); err == nil {
		t.Error("Prepare() left an interrupted download behind")
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Errorf("Prepare() removed an unrelated file from the handoff directory: %v", err)
	}
	if _, found, _ := ReadResult(fixture.stateDir); found {
		t.Error("Prepare() left the previous attempt's result in place, so the dashboard would report it as this attempt's")
	}
}

func TestManager_Prepare_HonoursACancelledContext(t *testing.T) {
	t.Parallel()

	fixture := newManagerFixture(t, "v0.1.3", "v0.1.4", nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := fixture.manager.Prepare(ctx); err == nil {
		t.Fatal("Prepare() ignored a cancelled context")
	}
	if _, err := os.Stat(RequestPath(fixture.stateDir)); err == nil {
		t.Fatal("Prepare() wrote a request after being cancelled")
	}
}

func TestNewManager_UsesTheInstalledLocations(t *testing.T) {
	t.Parallel()

	manager := NewManager("v0.1.3")
	if manager.stateDir != StateDir {
		t.Errorf("NewManager().stateDir = %q, want %q", manager.stateDir, StateDir)
	}
	if manager.platform != CurrentPlatform() {
		t.Errorf("NewManager().platform = %s, want %s", manager.platform, CurrentPlatform())
	}
	if manager.fetcher.BaseURL != DownloadBaseURL {
		t.Errorf("NewManager() fetcher base = %q, want %q", manager.fetcher.BaseURL, DownloadBaseURL)
	}
	if manager.currentRaw != "v0.1.3" || manager.current.Tag() != "v0.1.3" {
		t.Errorf("NewManager() current = %q / %s, want v0.1.3", manager.currentRaw, manager.current)
	}

	// A machine with no installation at all must come out unsupported rather
	// than offering an action that cannot work. No network call is made to find
	// that out.
	if environment := manager.Environment(); environment.Supported {
		t.Error("NewManager() reported an unprovisioned machine as supported")
	}
}

// Both endpoints are served concurrently by the same Manager: a status request
// can land while an upgrade is being prepared, and Prepare re-detects and
// replaces the environment while it runs. Under -race this fails if either side
// touches that state unguarded.
func TestManager_StatusAndPrepareAreSafeConcurrently(t *testing.T) {
	t.Parallel()

	fixture := newManagerFixture(t, "v0.1.3", "v0.1.4", nil)

	var waitGroup sync.WaitGroup
	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		if _, err := fixture.manager.Prepare(context.Background()); err != nil {
			t.Errorf("Prepare() error = %v", err)
		}
	}()

	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		for range 50 {
			status := fixture.manager.Status(context.Background())
			if status.CurrentVersion != "v0.1.3" {
				t.Errorf("Status().CurrentVersion = %q, want v0.1.3", status.CurrentVersion)
				return
			}
		}
	}()

	waitGroup.Wait()

	if _, _, err := ReadRequest(fixture.stateDir); err != nil {
		t.Fatalf("ReadRequest() after a concurrent prepare error = %v", err)
	}
}

func TestManager_CurrentVersion_IsWhatTheBinaryReports(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
		want    string
	}{
		{name: "a release tag", version: "v0.1.3", want: "v0.1.3"},
		{name: "surrounding whitespace is trimmed", version: "  v0.1.3 ", want: "v0.1.3"},
		{name: "a development build is reported as it is", version: "dev", want: "dev"},
		{name: "an empty version is reported as empty", version: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fixture := newManagerFixture(t, tt.version, "v0.1.4", map[string]map[string][]byte{})
			if got := fixture.manager.CurrentVersion(); got != tt.want {
				t.Fatalf("CurrentVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDefaultHTTPClientsAreUsedWhenNoneIsInjected(t *testing.T) {
	t.Parallel()

	if client := (&Source{}).httpClient(); client == nil || client.Timeout != CheckTimeout {
		t.Fatalf("Source.httpClient() = %+v, want a bounded default client", client)
	}
	if client := (&Fetcher{}).httpClient(); client == nil || client.Timeout != DownloadTimeout {
		t.Fatalf("Fetcher.httpClient() = %+v, want a bounded default client", client)
	}
	if fetcher := (&Applier{}).fetcher(); fetcher == nil || fetcher.BaseURL != DownloadBaseURL {
		t.Fatalf("Applier.fetcher() = %+v, want the pinned release download prefix", fetcher)
	}
}

// snapshotTree records the content and mode of every file under root.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()

	snapshot := make(map[string]string)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if info.IsDir() {
			snapshot[relative] = "dir " + info.Mode().Perm().String()
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		snapshot[relative] = info.Mode().Perm().String() + " " + digestOf(content)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return snapshot
}

func assertTreeUnchanged(t *testing.T, root string, before map[string]string) {
	t.Helper()

	after := snapshotTree(t, root)
	for path, state := range before {
		got, found := after[path]
		if !found {
			t.Errorf("%s was removed", path)
			continue
		}
		if got != state {
			t.Errorf("%s changed: %s -> %s", path, state, got)
		}
	}
	for path := range after {
		if _, found := before[path]; !found {
			t.Errorf("%s was created", path)
		}
	}
}
