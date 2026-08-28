package upgrade

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// maxVersionLength bounds the raw strings ParseVersion accepts. Real tags are
// a dozen characters; anything longer is not a version.
const maxVersionLength = 64

// ErrInvalidVersion is returned for strings that are not a strict semantic
// version.
var ErrInvalidVersion = errors.New("invalid version")

// Version is a parsed semantic version. Build metadata is rejected at parse
// time because release tags never carry it.
type Version struct {
	Prerelease string
	Major      int
	Minor      int
	Patch      int
}

// ParseVersion parses "MAJOR.MINOR.PATCH" with an optional leading "v" and an
// optional "-prerelease" suffix. Numeric identifiers must not have leading
// zeros and prerelease identifiers may only contain [0-9A-Za-z-].
func ParseVersion(raw string) (Version, error) {
	if len(raw) == 0 || len(raw) > maxVersionLength {
		return Version{}, fmt.Errorf("%w: %q", ErrInvalidVersion, raw)
	}
	body := strings.TrimPrefix(raw, "v")
	core, prerelease, hasPrerelease := strings.Cut(body, "-")

	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("%w: %q must have three numeric parts", ErrInvalidVersion, raw)
	}
	numbers := make([]int, 3)
	for i, part := range parts {
		number, err := parseNumericIdentifier(part)
		if err != nil {
			return Version{}, fmt.Errorf("%w: %q: %v", ErrInvalidVersion, raw, err)
		}
		numbers[i] = number
	}

	if hasPrerelease {
		if err := validatePrerelease(prerelease); err != nil {
			return Version{}, fmt.Errorf("%w: %q: %v", ErrInvalidVersion, raw, err)
		}
	}

	return Version{Major: numbers[0], Minor: numbers[1], Patch: numbers[2], Prerelease: prerelease}, nil
}

// ParseReleaseTag parses a release tag, which must carry the "v" prefix that
// release archives and download URLs are built from.
func ParseReleaseTag(tag string) (Version, error) {
	if !strings.HasPrefix(tag, "v") {
		return Version{}, fmt.Errorf("%w: release tag %q must start with v", ErrInvalidVersion, tag)
	}
	return ParseVersion(tag)
}

// parseNumericIdentifier parses a decimal identifier without leading zeros.
func parseNumericIdentifier(part string) (int, error) {
	if part == "" {
		return 0, errors.New("empty numeric identifier")
	}
	if len(part) > 9 {
		return 0, fmt.Errorf("numeric identifier %q is too long", part)
	}
	for _, r := range part {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("numeric identifier %q contains a non-digit", part)
		}
	}
	if len(part) > 1 && part[0] == '0' {
		return 0, fmt.Errorf("numeric identifier %q has a leading zero", part)
	}
	number, err := strconv.Atoi(part)
	if err != nil {
		return 0, fmt.Errorf("parse numeric identifier %q: %w", part, err)
	}
	return number, nil
}

// validatePrerelease checks the dot-separated identifiers after the hyphen.
func validatePrerelease(prerelease string) error {
	if prerelease == "" {
		return errors.New("empty prerelease")
	}
	for _, identifier := range strings.Split(prerelease, ".") {
		if identifier == "" {
			return errors.New("empty prerelease identifier")
		}
		numeric := true
		for _, r := range identifier {
			switch {
			case r >= '0' && r <= '9':
			case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r == '-':
				numeric = false
			default:
				return fmt.Errorf("prerelease identifier %q contains %q", identifier, r)
			}
		}
		if numeric && len(identifier) > 1 && identifier[0] == '0' {
			return fmt.Errorf("numeric prerelease identifier %q has a leading zero", identifier)
		}
	}
	return nil
}

// Core returns "MAJOR.MINOR.PATCH" without prefix or prerelease. Release
// archive names use this form.
func (v Version) Core() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Tag returns the release tag form, "vMAJOR.MINOR.PATCH[-prerelease]".
func (v Version) Tag() string {
	if v.Prerelease == "" {
		return "v" + v.Core()
	}
	return "v" + v.Core() + "-" + v.Prerelease
}

// String returns the tag form.
func (v Version) String() string {
	return v.Tag()
}

// IsStable reports whether the version has no prerelease suffix.
func (v Version) IsStable() bool {
	return v.Prerelease == ""
}

// Compare orders two versions by semantic version precedence. It returns -1
// when a is lower than b, 0 when equal and 1 when a is higher.
func Compare(a, b Version) int {
	switch {
	case a.Major != b.Major:
		return compareInt(a.Major, b.Major)
	case a.Minor != b.Minor:
		return compareInt(a.Minor, b.Minor)
	case a.Patch != b.Patch:
		return compareInt(a.Patch, b.Patch)
	}
	return comparePrerelease(a.Prerelease, b.Prerelease)
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// comparePrerelease implements the semver rule that a stable version is
// higher than any prerelease of the same core, numeric identifiers compare
// numerically and below alphanumeric ones, and a longer identifier list wins
// when every shared identifier is equal.
func comparePrerelease(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}

	left := strings.Split(a, ".")
	right := strings.Split(b, ".")
	for i := 0; i < len(left) && i < len(right); i++ {
		if left[i] == right[i] {
			continue
		}
		leftNumber, leftNumeric := parsePrereleaseNumber(left[i])
		rightNumber, rightNumeric := parsePrereleaseNumber(right[i])
		switch {
		case leftNumeric && rightNumeric:
			return compareInt(leftNumber, rightNumber)
		case leftNumeric:
			return -1
		case rightNumeric:
			return 1
		case left[i] < right[i]:
			return -1
		default:
			return 1
		}
	}
	return compareInt(len(left), len(right))
}

func parsePrereleaseNumber(identifier string) (int, bool) {
	number, err := parseNumericIdentifier(identifier)
	if err != nil {
		return 0, false
	}
	return number, true
}
