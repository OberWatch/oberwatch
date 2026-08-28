package upgrade

import (
	"errors"
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	t.Parallel()

	//nolint:govet // Keep table fields grouped by input, then expected parts.
	tests := []struct {
		name           string
		raw            string
		wantMajor      int
		wantMinor      int
		wantPatch      int
		wantPrerelease string
		wantErr        bool
	}{
		{name: "plain core version", raw: "1.2.3", wantMajor: 1, wantMinor: 2, wantPatch: 3},
		{name: "v prefixed core version", raw: "v1.2.3", wantMajor: 1, wantMinor: 2, wantPatch: 3},
		{name: "zero version", raw: "v0.0.0", wantMajor: 0, wantMinor: 0, wantPatch: 0},
		{name: "prerelease suffix", raw: "v0.1.4-rc.1", wantMajor: 0, wantMinor: 1, wantPatch: 4, wantPrerelease: "rc.1"},
		{name: "prerelease with hyphen in identifier", raw: "v1.0.0-alpha-1", wantMajor: 1, wantPrerelease: "alpha-1"},

		{name: "empty", raw: "", wantErr: true},
		{name: "only a v", raw: "v", wantErr: true},
		{name: "two parts", raw: "v1.2", wantErr: true},
		{name: "four parts", raw: "v1.2.3.4", wantErr: true},
		{name: "leading zero in minor", raw: "v1.02.3", wantErr: true},
		{name: "non numeric patch", raw: "v1.2.x", wantErr: true},
		{name: "negative", raw: "v-1.2.3", wantErr: true},
		{name: "build metadata is rejected", raw: "v1.2.3+build.5", wantErr: true},
		{name: "empty prerelease", raw: "v1.2.3-", wantErr: true},
		{name: "empty prerelease identifier", raw: "v1.2.3-rc..1", wantErr: true},
		{name: "prerelease with a slash", raw: "v1.2.3-rc/1", wantErr: true},
		{name: "leading zero numeric prerelease", raw: "v1.2.3-01", wantErr: true},
		{name: "whitespace is not trimmed away", raw: " v1.2.3", wantErr: true},
		{name: "shell metacharacters", raw: "v1.2.3; rm -rf /", wantErr: true},
		{name: "path traversal", raw: "../../etc/passwd", wantErr: true},
		{name: "newline injection", raw: "v1.2.3\nv9.9.9", wantErr: true},
		{name: "null byte", raw: "v1.2.3\x00", wantErr: true},
		{name: "over the length bound", raw: "v1.2.3-" + strings.Repeat("a", maxVersionLength), wantErr: true},
		{name: "numeric identifier over the digit bound", raw: "v1234567890.0.0", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseVersion(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseVersion(%q) = %+v, want an error", tt.raw, got)
				}
				if !errors.Is(err, ErrInvalidVersion) {
					t.Fatalf("ParseVersion(%q) error = %v, want ErrInvalidVersion", tt.raw, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseVersion(%q) error = %v", tt.raw, err)
			}
			if got.Major != tt.wantMajor || got.Minor != tt.wantMinor || got.Patch != tt.wantPatch {
				t.Fatalf("ParseVersion(%q) = %d.%d.%d, want %d.%d.%d", tt.raw, got.Major, got.Minor, got.Patch, tt.wantMajor, tt.wantMinor, tt.wantPatch)
			}
			if got.Prerelease != tt.wantPrerelease {
				t.Fatalf("ParseVersion(%q).Prerelease = %q, want %q", tt.raw, got.Prerelease, tt.wantPrerelease)
			}
		})
	}
}

func TestParseReleaseTag_RequiresTheVPrefix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "release tag", raw: "v0.1.4"},
		{name: "prerelease tag", raw: "v0.1.4-rc.1"},
		{name: "no prefix", raw: "0.1.4", wantErr: true},
		{name: "uppercase prefix", raw: "V0.1.4", wantErr: true},
		{name: "refs path", raw: "refs/tags/v0.1.4", wantErr: true},
		{name: "empty", raw: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := ParseReleaseTag(tt.raw)
			if tt.wantErr != (err != nil) {
				t.Fatalf("ParseReleaseTag(%q) error = %v, wantErr %v", tt.raw, err, tt.wantErr)
			}
		})
	}
}

func TestVersion_Formatting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		raw        string
		wantCore   string
		wantTag    string
		wantStable bool
	}{
		{name: "stable", raw: "v0.1.4", wantCore: "0.1.4", wantTag: "v0.1.4", wantStable: true},
		{name: "prerelease", raw: "v0.1.4-rc.1", wantCore: "0.1.4", wantTag: "v0.1.4-rc.1"},
		{name: "no prefix in, prefix out", raw: "1.0.0", wantCore: "1.0.0", wantTag: "v1.0.0", wantStable: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			version, err := ParseVersion(tt.raw)
			if err != nil {
				t.Fatalf("ParseVersion(%q) error = %v", tt.raw, err)
			}
			if got := version.Core(); got != tt.wantCore {
				t.Errorf("Core() = %q, want %q", got, tt.wantCore)
			}
			if got := version.Tag(); got != tt.wantTag {
				t.Errorf("Tag() = %q, want %q", got, tt.wantTag)
			}
			if got := version.String(); got != tt.wantTag {
				t.Errorf("String() = %q, want %q", got, tt.wantTag)
			}
			if got := version.IsStable(); got != tt.wantStable {
				t.Errorf("IsStable() = %v, want %v", got, tt.wantStable)
			}
		})
	}
}

func TestCompare(t *testing.T) {
	t.Parallel()

	//nolint:govet // Keep table fields grouped by input, then expected forms.
	tests := []struct {
		name string
		left string
		want int

		right string
	}{
		{name: "equal", left: "v1.2.3", right: "v1.2.3", want: 0},
		{name: "major lower", left: "v1.0.0", right: "v2.0.0", want: -1},
		{name: "major higher", left: "v2.0.0", right: "v1.9.9", want: 1},
		{name: "minor lower", left: "v1.1.0", right: "v1.2.0", want: -1},
		{name: "patch higher", left: "v1.1.2", right: "v1.1.1", want: 1},
		{name: "double digit patch beats single digit", left: "v0.1.10", right: "v0.1.9", want: 1},
		{name: "double digit minor beats single digit", left: "v0.10.0", right: "v0.9.9", want: 1},
		{name: "stable beats its own prerelease", left: "v1.0.0", right: "v1.0.0-rc.1", want: 1},
		{name: "prerelease loses to its own stable", left: "v1.0.0-rc.1", right: "v1.0.0", want: -1},
		{name: "numeric prerelease compares numerically", left: "v1.0.0-rc.2", right: "v1.0.0-rc.10", want: -1},
		{name: "numeric prerelease sorts below alphanumeric", left: "v1.0.0-1", right: "v1.0.0-alpha", want: -1},
		{name: "alphanumeric prerelease compares as text", left: "v1.0.0-alpha", right: "v1.0.0-beta", want: -1},
		{name: "longer prerelease list wins on equal prefix", left: "v1.0.0-alpha.1", right: "v1.0.0-alpha", want: 1},
		{name: "equal prerelease", left: "v1.0.0-rc.1", right: "v1.0.0-rc.1", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			left := mustParseVersion(t, tt.left)
			right := mustParseVersion(t, tt.right)

			if got := Compare(left, right); got != tt.want {
				t.Fatalf("Compare(%s, %s) = %d, want %d", tt.left, tt.right, got, tt.want)
			}
			if got := Compare(right, left); got != -tt.want {
				t.Fatalf("Compare(%s, %s) = %d, want %d (comparison must be symmetric)", tt.right, tt.left, got, -tt.want)
			}
		})
	}
}

func mustParseVersion(t *testing.T, raw string) Version {
	t.Helper()

	version, err := ParseVersion(raw)
	if err != nil {
		t.Fatalf("ParseVersion(%q) error = %v", raw, err)
	}
	return version
}
