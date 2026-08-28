package upgrade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// supportedDetector builds a Detector over a temporary directory tree that looks
// like an installation the installer has provisioned.
func supportedDetector(t *testing.T) *Detector {
	t.Helper()

	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	unit := filepath.Join(root, "oberwatch-upgrade.service")
	if err := os.WriteFile(unit, []byte("[Service]\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	return &Detector{
		StateDir:         state,
		ApplyUnitPath:    unit,
		ContainerMarkers: []string{filepath.Join(root, "no-such-container-marker")},
		Platform:         Platform{OS: "linux", Arch: "amd64"},
	}
}

func TestDetector_Detect_SupportedInstallation(t *testing.T) {
	t.Parallel()

	environment := supportedDetector(t).Detect("v0.1.3")
	if !environment.Supported {
		t.Fatalf("Detect() = %+v, want a provisioned release installation to be supported", environment)
	}
	if environment.Reason != "" || environment.Fallback != "" {
		t.Fatalf("Detect() = %+v, want no reason or fallback when supported", environment)
	}
}

func TestDetector_Detect_UnsupportedInstallations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		version      string
		mutate       func(t *testing.T, detector *Detector)
		wantFallback string
		wantReason   string
	}{
		{
			name:    "a platform with no release archive",
			version: "v0.1.3",
			mutate: func(_ *testing.T, detector *Detector) {
				detector.Platform = Platform{OS: "darwin", Arch: "arm64"}
			},
			wantFallback: PlatformFallback,
			wantReason:   "darwin/arm64",
		},
		{
			name:    "a 32-bit platform",
			version: "v0.1.3",
			mutate: func(_ *testing.T, detector *Detector) {
				detector.Platform = Platform{OS: "linux", Arch: "386"}
			},
			wantFallback: PlatformFallback,
		},
		{
			name:    "inside a container",
			version: "v0.1.3",
			mutate: func(t *testing.T, detector *Detector) {
				marker := filepath.Join(t.TempDir(), ".dockerenv")
				if err := os.WriteFile(marker, nil, 0o644); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
				detector.ContainerMarkers = []string{marker}
			},
			wantFallback: ContainerFallback,
			wantReason:   "container",
		},
		{
			name:         "a build that did not come from a release",
			version:      "dev",
			mutate:       func(_ *testing.T, _ *Detector) {},
			wantFallback: SourceFallback,
			wantReason:   "not a release tag",
		},
		{
			name:         "a version without the release tag prefix",
			version:      "0.1.3",
			mutate:       func(_ *testing.T, _ *Detector) {},
			wantFallback: SourceFallback,
		},
		{
			name:         "an empty version",
			version:      "",
			mutate:       func(_ *testing.T, _ *Detector) {},
			wantFallback: SourceFallback,
		},
		{
			name:    "the privileged applier is not installed",
			version: "v0.1.3",
			mutate: func(t *testing.T, detector *Detector) {
				if err := os.Remove(detector.ApplyUnitPath); err != nil {
					t.Fatalf("Remove() error = %v", err)
				}
			},
			wantFallback: InstallerFallback,
			wantReason:   "privileged upgrade applier is not installed",
		},
		{
			name:    "the handoff directory does not exist",
			version: "v0.1.3",
			mutate: func(t *testing.T, detector *Detector) {
				if err := os.Remove(detector.StateDir); err != nil {
					t.Fatalf("Remove() error = %v", err)
				}
			},
			wantFallback: InstallerFallback,
		},
		{
			name:    "the handoff directory is world-writable",
			version: "v0.1.3",
			mutate: func(t *testing.T, detector *Detector) {
				if err := os.Chmod(detector.StateDir, 0o777); err != nil {
					t.Fatalf("Chmod() error = %v", err)
				}
			},
			wantFallback: InstallerFallback,
			wantReason:   "writable",
		},
		{
			name:    "the handoff directory is a file",
			version: "v0.1.3",
			mutate: func(t *testing.T, detector *Detector) {
				if err := os.Remove(detector.StateDir); err != nil {
					t.Fatalf("Remove() error = %v", err)
				}
				if err := os.WriteFile(detector.StateDir, nil, 0o600); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
			},
			wantFallback: InstallerFallback,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			detector := supportedDetector(t)
			tt.mutate(t, detector)

			environment := detector.Detect(tt.version)
			if environment.Supported {
				t.Fatalf("Detect() = %+v, want an unsupported installation", environment)
			}
			if environment.Reason == "" {
				t.Fatal("Detect() gave no reason; an unsupported installation has to say why")
			}
			if environment.Fallback != tt.wantFallback {
				t.Fatalf("Detect().Fallback = %q, want %q", environment.Fallback, tt.wantFallback)
			}
			if tt.wantReason != "" && !strings.Contains(environment.Reason, tt.wantReason) {
				t.Fatalf("Detect().Reason = %q, want it to mention %q", environment.Reason, tt.wantReason)
			}
		})
	}
}

func TestDetector_Detect_LeavesNoWriteProbeBehind(t *testing.T) {
	t.Parallel()

	detector := supportedDetector(t)
	for range 3 {
		if environment := detector.Detect("v0.1.3"); !environment.Supported {
			t.Fatalf("Detect() = %+v, want supported", environment)
		}
	}

	entries, err := os.ReadDir(detector.StateDir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("the handoff directory holds %d entries after detection, want the write probe cleaned up", len(entries))
	}
}

func TestFallbackInstructions_AreRealAndActionable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		instruction  string
		wantContains string
	}{
		{name: "installer fallback names the installer", instruction: InstallerFallback, wantContains: "scripts/install.sh"},
		{name: "container fallback names an image pull", instruction: ContainerFallback, wantContains: "docker pull"},
		{name: "source fallback names a rebuild", instruction: SourceFallback, wantContains: "make build"},
		{name: "platform fallback names the supported platforms", instruction: PlatformFallback, wantContains: "arm64"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if !strings.Contains(tt.instruction, tt.wantContains) {
				t.Fatalf("%q does not mention %q", tt.instruction, tt.wantContains)
			}
			if strings.Contains(tt.instruction, "sudo rm") {
				t.Fatalf("%q suggests a destructive command", tt.instruction)
			}
		})
	}
}

func TestNewDetector_UsesTheInstalledLocations(t *testing.T) {
	t.Parallel()

	detector := NewDetector()
	if detector.StateDir != StateDir {
		t.Errorf("NewDetector().StateDir = %q, want %q", detector.StateDir, StateDir)
	}
	if detector.ApplyUnitPath != ApplyUnitPath {
		t.Errorf("NewDetector().ApplyUnitPath = %q, want %q", detector.ApplyUnitPath, ApplyUnitPath)
	}
	if detector.Platform != CurrentPlatform() {
		t.Errorf("NewDetector().Platform = %s, want %s", detector.Platform, CurrentPlatform())
	}
	if len(detector.ContainerMarkers) == 0 {
		t.Error("NewDetector() has no container markers, so a container would be treated as a normal install")
	}
}
