package upgrade

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrUnsupported means this installation cannot apply an upgrade in place. It
// is not a failure: it is the condition under which the dashboard shows a
// fallback instruction instead of a button.
var ErrUnsupported = errors.New("in-dashboard upgrade is not supported for this installation")

// Fallback instructions. Each one is the real command for the installation it
// describes, so an operator who cannot use the button is still told exactly how
// to get the new version.
const (
	// InstallerFallback re-runs the installer, which upgrades in place and
	// keeps the existing config and data.
	InstallerFallback = "Re-run the installer to upgrade: curl -fsSL https://raw.githubusercontent.com/OberWatch/oberwatch/main/scripts/install.sh | sh"

	// ContainerFallback covers a container, where replacing the binary inside
	// the running container would be undone by the next recreate.
	ContainerFallback = "Pull the new image and recreate the container: docker pull ghcr.io/oberwatch/oberwatch:latest"

	// SourceFallback covers a build that did not come from a release.
	SourceFallback = "This build did not come from a release. Check out the release tag and rebuild with: make build"

	// PlatformFallback covers a platform with no release archive.
	PlatformFallback = "Releases ship Linux amd64 and arm64 binaries only. Run Oberwatch in Docker on this platform: docker pull ghcr.io/oberwatch/oberwatch:latest"
)

// Environment reports whether an in-dashboard upgrade can be applied on this
// installation, and what to do instead when it cannot.
//
//nolint:govet // Keep fields grouped by what they describe, not by width.
type Environment struct {
	// Supported is true only when every condition for applying an upgrade in
	// place is met. It is never assumed: each condition is checked.
	Supported bool

	// Reason states why an upgrade cannot be applied here. It is empty when
	// Supported is true.
	Reason string

	// Fallback is the instruction shown in place of the upgrade action.
	Fallback string
}

// Detector inspects the pieces the installer provisions to decide whether an
// upgrade can be applied in place.
//
// Every location it looks at is a package constant. The zero value is not
// usable; build one with NewDetector.
//
//nolint:govet // Keep fields grouped by what they describe, not by width.
type Detector struct {
	// StateDir is the handoff directory the server must be able to write.
	StateDir string

	// ApplyUnitPath is the privileged applier unit. Without it, a request would
	// be written and never acted on, so the action must not be offered.
	ApplyUnitPath string

	// ContainerMarkers are the files whose presence means this process is in a
	// container.
	ContainerMarkers []string

	// Platform is the release platform this binary was built for.
	Platform Platform
}

// NewDetector builds a Detector over the installed locations.
func NewDetector() *Detector {
	return &Detector{
		StateDir:         StateDir,
		ApplyUnitPath:    ApplyUnitPath,
		ContainerMarkers: []string{"/.dockerenv", "/run/.containerenv"},
		Platform:         CurrentPlatform(),
	}
}

// Detect reports whether the running installation can apply an upgrade for the
// given running version.
//
// The checks run from the most specific fallback to the least, so an operator
// is told the thing that actually applies to them: a container is told to pull
// an image, a source build is told to rebuild, and a release install that is
// simply missing the privileged applier is told to re-run the installer.
func (d *Detector) Detect(currentVersion string) Environment {
	if !d.Platform.Supported() {
		return unsupported(
			fmt.Sprintf("no release archive is published for %s", d.Platform),
			PlatformFallback,
		)
	}
	if marker, inContainer := d.containerMarker(); inContainer {
		return unsupported(
			fmt.Sprintf("running in a container (%s); replacing the binary would be lost on the next recreate", marker),
			ContainerFallback,
		)
	}
	if _, err := ParseReleaseTag(currentVersion); err != nil {
		return unsupported(
			fmt.Sprintf("the running version %q is not a release tag", currentVersion),
			SourceFallback,
		)
	}
	if err := d.checkApplyUnit(); err != nil {
		return unsupported(err.Error(), InstallerFallback)
	}
	if err := d.checkStateDir(); err != nil {
		return unsupported(err.Error(), InstallerFallback)
	}
	return Environment{Supported: true}
}

func unsupported(reason string, fallback string) Environment {
	return Environment{Reason: reason, Fallback: fallback}
}

func (d *Detector) containerMarker() (string, bool) {
	for _, marker := range d.ContainerMarkers {
		if _, err := os.Stat(marker); err == nil {
			return marker, true
		}
	}
	return "", false
}

// checkApplyUnit requires the privileged applier unit to be installed. Its
// presence is what makes an upgrade request something that will actually be
// acted on rather than a file nobody reads.
func (d *Detector) checkApplyUnit() error {
	if d.ApplyUnitPath == "" {
		return errors.New("the privileged upgrade applier is not installed")
	}
	if _, err := os.Stat(d.ApplyUnitPath); err != nil {
		return fmt.Errorf("the privileged upgrade applier is not installed at %s", d.ApplyUnitPath)
	}
	return nil
}

// checkStateDir requires the handoff directory to exist, to be safe to trust,
// and to be writable by this process. Writability is tested by writing, not
// inferred from the mode bits, so the answer is the real one.
func (d *Detector) checkStateDir() error {
	if d.StateDir == "" {
		return errors.New("no upgrade handoff directory is configured")
	}
	if err := requireSafeDir(d.StateDir); err != nil {
		return err
	}

	probe, err := os.CreateTemp(d.StateDir, ".writable-*")
	if err != nil {
		return fmt.Errorf("the upgrade handoff directory %s is not writable by this service", d.StateDir)
	}
	name := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("close write probe in %s: %w", d.StateDir, err)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("remove write probe %s: %w", filepath.Base(name), err)
	}
	return nil
}
