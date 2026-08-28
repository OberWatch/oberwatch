package scripts

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/OberWatch/oberwatch/internal/upgrade"
)

// installerAssignment reads a top-level `NAME="value"` assignment out of the
// installer.
func installerAssignment(t *testing.T, name string) string {
	t.Helper()

	contents, err := os.ReadFile(installerPath(t))
	if err != nil {
		t.Fatalf("read installer: %v", err)
	}

	pattern := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `="([^"]*)"$`)
	match := pattern.FindSubmatch(contents)
	if match == nil {
		t.Fatalf("install.sh has no %s= assignment", name)
	}
	return string(match[1])
}

// The installer and internal/upgrade both hard-code the handoff locations, and
// they have to be the same locations: if they drift, the service writes a
// request nothing reads, or the dashboard reports an installation as upgradable
// when it is not. Nothing here is configurable on purpose — a request must never
// be able to name a path.
func TestInstallerUpgradePathsMatchTheGoConstants(t *testing.T) {
	t.Parallel()

	stateDir := installerAssignment(t, "UPGRADE_STATE_DIR")
	requestPath := installerAssignment(t, "UPGRADE_REQUEST_PATH")
	upgradeServiceName := installerAssignment(t, "UPGRADE_SERVICE_NAME")
	installPath := installerAssignment(t, "INSTALL_PATH")
	serviceName := installerAssignment(t, "SERVICE_NAME")

	tests := []struct {
		name string
		got  string
		want string
	}{
		{name: "handoff directory", got: stateDir, want: upgrade.StateDir},
		{name: "request file", got: strings.Replace(requestPath, "${UPGRADE_STATE_DIR}", stateDir, 1), want: upgrade.RequestPath(upgrade.StateDir)},
		{name: "applier unit", got: "/etc/systemd/system/" + upgradeServiceName + ".service", want: upgrade.ApplyUnitPath},
		{name: "restarted service", got: serviceName, want: upgrade.ServiceName},
		{name: "installed binary name", got: filepath.Base(installPath), want: upgrade.BinaryName},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.got != tt.want {
				t.Fatalf("installer %s = %q, want %q (internal/upgrade and install.sh must agree)", tt.name, tt.got, tt.want)
			}
		})
	}
}

// runUpgradeUnitWriter runs the installer's write_upgrade_units with tee stubbed
// so both generated units land in tempDir.
func runUpgradeUnitWriter(t *testing.T) (serviceUnit string, pathUnit string) {
	t.Helper()

	tempDir := t.TempDir()
	library := installerLibrary(t)
	snippet := `. "$1"
INSTALL_PATH=/usr/local/bin/oberwatch
UPGRADE_SERVICE_NAME=oberwatch-upgrade
UPGRADE_STATE_DIR=/var/lib/oberwatch/upgrade
UPGRADE_REQUEST_PATH=${UPGRADE_STATE_DIR}/request.json
OUT_DIR=$2
sudo_cmd() { "$@"; }
tee() { cat >"$OUT_DIR/$(basename "$1")"; }
write_upgrade_units
`
	output, err := runShellSnippet(t, snippet, library, tempDir)
	if err != nil {
		t.Fatalf("write_upgrade_units failed: %v\n%s", err, output)
	}

	service, err := os.ReadFile(filepath.Join(tempDir, "oberwatch-upgrade.service"))
	if err != nil {
		t.Fatalf("read generated applier unit: %v", err)
	}
	path, err := os.ReadFile(filepath.Join(tempDir, "oberwatch-upgrade.path"))
	if err != nil {
		t.Fatalf("read generated path unit: %v", err)
	}
	return string(service), string(path)
}

func TestWriteUpgradeUnitsProvisionsARootApplierWithNoArguments(t *testing.T) {
	t.Parallel()

	serviceUnit, pathUnit := runUpgradeUnitWriter(t)

	tests := []struct {
		name string
		unit string
		want string
	}{
		{name: "the applier is a one-shot", unit: serviceUnit, want: "Type=oneshot"},
		{name: "the applier runs as root", unit: serviceUnit, want: "User=root"},
		{name: "the applier is the installed binary", unit: serviceUnit, want: "ExecStart=/usr/local/bin/oberwatch upgrade apply"},
		{name: "the path unit watches the request file", unit: pathUnit, want: "PathExists=/var/lib/oberwatch/upgrade/request.json"},
		{name: "the path unit triggers the applier", unit: pathUnit, want: "Unit=oberwatch-upgrade.service"},
		{name: "the path unit is enabled at boot", unit: pathUnit, want: "WantedBy=multi-user.target"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if !strings.Contains(tt.unit, tt.want) {
				t.Fatalf("generated unit does not contain %q:\n%s", tt.want, tt.unit)
			}
		})
	}

	// ExecStart is the one line that runs as root, so it must be exactly the
	// installed binary and the two fixed words: no shell, no wrapper, and no
	// argument that could carry a version, tag, URL or path.
	execStart := regexp.MustCompile(`(?m)^ExecStart=(.*)$`).FindStringSubmatch(serviceUnit)
	if execStart == nil {
		t.Fatalf("generated applier unit has no ExecStart line:\n%s", serviceUnit)
	}
	if fields := strings.Fields(execStart[1]); len(fields) != 3 {
		t.Fatalf("ExecStart = %q, want exactly the binary plus \"upgrade apply\"", execStart[1])
	}
	for _, forbidden := range []string{"/bin/sh", "/bin/bash", "-c", "curl", "$", "|", "&&", ";", "%i"} {
		if strings.Contains(execStart[1], forbidden) {
			t.Fatalf("ExecStart = %q, must not contain %q", execStart[1], forbidden)
		}
	}
	if strings.Contains(pathUnit, "PathChanged") || strings.Contains(pathUnit, "DirectoryNotEmpty") {
		t.Fatalf("path unit must trigger on the request file alone:\n%s", pathUnit)
	}
	// A templated unit would let the trigger choose an instance name; the applier
	// must be a single fixed unit.
	if strings.Contains(serviceUnit, "@") || strings.Contains(pathUnit, "@") {
		t.Fatalf("upgrade units must not be templated:\nservice:\n%s\npath:\n%s", serviceUnit, pathUnit)
	}
}

func TestSetupUpgradeHandoffIsOwnerOnlyAndServiceOwned(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	callsPath := filepath.Join(tempDir, "calls")

	library := installerLibrary(t)
	snippet := fmt.Sprintf(`. "$1"
LINUX_SERVICE_USER=oberwatch
UPGRADE_STATE_DIR=/var/lib/oberwatch/upgrade
sudo_cmd() {
  printf '%%s\n' "$*" >>%q
  return 0
}
setup_upgrade_handoff
`, callsPath)

	output, err := runShellSnippet(t, snippet, library)
	if err != nil {
		t.Fatalf("setup_upgrade_handoff failed: %v\n%s", err, output)
	}

	calls, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatalf("read privileged calls: %v", err)
	}

	want := strings.Join([]string{
		"mkdir -p /var/lib/oberwatch/upgrade",
		"chown oberwatch:oberwatch /var/lib/oberwatch/upgrade",
		"chmod 0700 /var/lib/oberwatch/upgrade",
		// A stale request from an earlier session must not be applied and
		// reported as this install's failure.
		"rm -f /var/lib/oberwatch/upgrade/request.json",
	}, "\n") + "\n"
	if string(calls) != want {
		t.Fatalf("setup_upgrade_handoff ran:\n%s\nwant:\n%s", calls, want)
	}
}

// A group- or world-writable handoff directory would let a local user stage an
// archive or a request for the root applier, so both halves refuse one. This
// pins that the mode the installer sets is a mode they accept.
func TestInstallerHandoffModeIsAcceptedByTheUpgradePackage(t *testing.T) {
	t.Parallel()

	contents, err := os.ReadFile(installerPath(t))
	if err != nil {
		t.Fatalf("read installer: %v", err)
	}
	match := regexp.MustCompile(`chmod (\d+) "\$\{UPGRADE_STATE_DIR\}"`).FindSubmatch(contents)
	if match == nil {
		t.Fatal("install.sh does not set an explicit mode on the upgrade handoff directory")
	}

	mode := string(match[1])
	if mode != "0700" {
		t.Fatalf("handoff directory mode = %s, want 0700", mode)
	}

	dir := filepath.Join(t.TempDir(), "upgrade")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	detector := &upgrade.Detector{
		StateDir:      dir,
		ApplyUnitPath: writeStubApplyUnit(t),
		Platform:      upgrade.Platform{OS: "linux", Arch: "amd64"},
	}
	if environment := detector.Detect("v0.1.3"); !environment.Supported {
		t.Fatalf("Detect() = %+v, want the mode the installer sets to be accepted", environment)
	}
}

func TestUninstallerRemovesTheUpgradeMachinery(t *testing.T) {
	t.Parallel()

	contents, err := os.ReadFile(filepath.Join(filepath.Dir(installerPath(t)), "uninstall.sh"))
	if err != nil {
		t.Fatalf("read uninstaller: %v", err)
	}
	uninstaller := string(contents)

	for _, want := range []string{
		"oberwatch-upgrade",
		"/var/lib/oberwatch",
		"${INSTALL_PATH}.previous",
	} {
		if !strings.Contains(uninstaller, want) {
			t.Errorf("uninstall.sh does not remove %q", want)
		}
	}
}

// TestEmbeddedDashboardUpgradeActionContract guards the committed bundle that
// GoReleaser embeds. `make dashboard` has to be rerun after a dashboard change,
// or the shipped binary serves a UI without the upgrade action while the API
// offers it. Identifiers are minified, so this pins the strings and paths that
// survive minification rather than any generated name.
func TestEmbeddedDashboardUpgradeActionContract(t *testing.T) {
	t.Parallel()

	bundle := embeddedDashboardBundle(t)

	tests := []struct {
		name string
		want string
	}{
		{name: "the action label", want: "Upgrade to "},
		{name: "the authenticated status endpoint", want: "/upgrade/status"},
		{name: "the restart disclosure", want: "The service then restarts"},
		{name: "the data safety disclosure", want: "database are not touched"},
		{name: "the rollback disclosure", want: "rolled back"},
		{name: "the unsupported fallback wording", want: "not available for this installation"},
		{name: "the loading state", want: "Checking for updates"},
		{name: "the failed-check state", want: "Update check unavailable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if !strings.Contains(bundle, tt.want) {
				t.Errorf("embedded dashboard bundle is missing %q; rerun `make dashboard`", tt.want)
			}
		})
	}
}

// The bundle must not carry a way to ask for a specific release. The upgrade
// request is a bodyless POST, and a body naming a tag or a URL appearing here
// would mean the UI grew one.
func TestEmbeddedDashboardUpgradeRequestCarriesNoTarget(t *testing.T) {
	t.Parallel()

	bundle := embeddedDashboardBundle(t)

	pattern := regexp.MustCompile(`"/upgrade"[^)]{0,200}?body`)
	if pattern.MatchString(bundle) {
		t.Error("the embedded bundle sends a body with the upgrade request; the release installed must come from the server's own check")
	}
}

func embeddedDashboardBundle(t *testing.T) string {
	t.Helper()

	staticRoot := filepath.Join(filepath.Dir(filepath.Dir(installerPath(t))), "internal", "dashboard", "static")
	var bundle strings.Builder
	walkErr := filepath.WalkDir(staticRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".js" {
			return nil
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		bundle.Write(contents)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk embedded dashboard assets: %v", walkErr)
	}
	return bundle.String()
}

func writeStubApplyUnit(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "oberwatch-upgrade.service")
	if err := os.WriteFile(path, []byte("[Service]\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}
