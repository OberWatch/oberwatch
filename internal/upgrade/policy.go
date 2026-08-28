package upgrade

// IsUpgrade reports whether latest is a version this installation should be
// offered as an upgrade from current.
//
// The policy is deliberately narrow, because the button it drives triggers a
// service restart:
//
//   - Only a stable release is ever offered. A prerelease is never an upgrade
//     target, whatever the running version is.
//   - The candidate must be strictly higher than the running version by
//     semantic version precedence, so an equal or older release offers nothing.
//
// A running prerelease is still allowed to move to the stable release of the
// same core version, because semver precedence already places v0.1.4 above
// v0.1.4-rc.1.
func IsUpgrade(current, latest Version) bool {
	if !latest.IsStable() {
		return false
	}
	return Compare(latest, current) > 0
}
