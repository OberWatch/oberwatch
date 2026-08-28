// Package upgrade implements the in-dashboard upgrade flow.
//
// The flow is split across two trust boundaries.
//
// The running server is unprivileged and network-facing. It reads the public
// release metadata endpoint for the newest stable release, caches that answer,
// and decides whether it is newer than the running version. When an operator
// confirms an upgrade it downloads the release archive for its own platform,
// verifies it against the SHA-256 published with the release, stages it in a
// handoff directory, and writes a request file naming the validated version. It
// never replaces a binary and never restarts anything.
//
// The privileged applier ("oberwatch upgrade apply", started by a systemd path
// unit when the request file appears) does the rest. It re-parses the requested
// version, refuses anything that is not strictly newer than the version it is
// itself built as, fetches the release checksums itself from the pinned release
// host, re-verifies the staged archive against them, extracts the single
// "oberwatch" member, checks that the extracted binary runs and reports the
// requested version, keeps a rollback copy of the binary it is replacing, swaps
// the install path with an atomic rename, records the outcome, and restarts the
// service.
//
// Two properties follow from that split, and both are what make the flow safe
// to expose in a dashboard:
//
//   - Nothing the flow reads, writes, fetches or executes is derived from a
//     request. Every URL, path, archive name and command comes from a package
//     constant plus a strictly parsed semantic version. There is no parameter to
//     inject a tag, URL, path or shell fragment into.
//   - The privileged half does not trust the unprivileged half. It establishes
//     the authenticity of what it installs from the release checksums it fetches
//     itself. A compromised server can at most cause a genuine, newer, published
//     release to be installed.
//
// Configuration and data are outside the flow entirely. The only paths written
// are the install path, a rollback copy next to it, and the handoff directory.
//
// An installation that cannot apply an upgrade in place — a container, a build
// that did not come from a release, a platform with no release archive, or an
// install without the privileged applier — is reported as unsupported with the
// real fallback instruction for that case, rather than being shown an action
// that would not work.
package upgrade
