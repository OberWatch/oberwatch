#!/usr/bin/env bash
# Runs the three release smoke contracts against one oberwatch binary:
#   init/validate        scripts/test-cli-config.sh
#   runaway kill/enable  scripts/dev/test-runaway.sh
#   task budgets         scripts/dev/test-task-budget.sh
#
# Usage: scripts/release-smoke.sh [path-to-oberwatch-binary]
# Without an argument the script builds ./cmd/oberwatch with the same
# -X main.version ldflag GoReleaser uses, so the run mirrors a release binary.
# Pass a downloaded release binary to check the published artifact.
#
# The binary must report the newest CHANGELOG release, so a stale binary or a
# missed version bump fails before any contract runs. Every contract runs even
# if an earlier one fails. Each transcript is printed and saved under
# RELEASE_SMOKE_DIR; without it a temp dir is used and removed on exit.
set -euo pipefail

# Resolve a caller-supplied binary against the caller's directory before
# moving to the repository root.
BINARY_ARG="${1:-}"
case "$BINARY_ARG" in
  "" | /*) ;;
  *) BINARY_ARG="$(pwd)/$BINARY_ARG" ;;
esac

# Same for the transcript directory, so a relative RELEASE_SMOKE_DIR lands
# where the caller asked instead of under the repository root.
case "${RELEASE_SMOKE_DIR:-}" in
  "" | /*) ;;
  *) RELEASE_SMOKE_DIR="$(pwd)/$RELEASE_SMOKE_DIR" ;;
esac

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# The newest released CHANGELOG heading is the same one scripts/version_test.go
# pins every version surface to.
# `|| true` keeps a CHANGELOG without a released heading on the reported path:
# under pipefail the failing grep would otherwise exit before the message.
VERSION="$(grep -m1 -E '^## \[[0-9]+\.[0-9]+\.[0-9]+\] - [0-9]{4}-[0-9]{2}-[0-9]{2}$' CHANGELOG.md \
  | sed -E 's/^## \[([^]]+)\].*/\1/' || true)"
[ -n "$VERSION" ] || fail "CHANGELOG.md has no released section heading"

KEEP_TRANSCRIPTS=1
if [ -z "${RELEASE_SMOKE_DIR:-}" ]; then
  RELEASE_SMOKE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/oberwatch-release-smoke.XXXXXX")"
  KEEP_TRANSCRIPTS=0
fi
mkdir -p "$RELEASE_SMOKE_DIR"
cleanup() {
  if [ "$KEEP_TRANSCRIPTS" -eq 0 ]; then
    rm -rf "$RELEASE_SMOKE_DIR"
  fi
}
trap cleanup EXIT

find_go() {
  if command -v go >/dev/null 2>&1; then
    command -v go
    return
  fi
  local candidate
  for candidate in /usr/local/go/bin/go "$HOME/.local/go/bin/go" "$HOME/go/bin/go"; do
    if [ -x "$candidate" ]; then
      echo "$candidate"
      return
    fi
  done
  fail "go toolchain not found"
}

BINARY="$BINARY_ARG"
if [ -z "$BINARY" ]; then
  GO="$(find_go)"
  export GOCACHE="${GOCACHE:-/tmp/go-build}"
  # Match the GoReleaser builds, which are static and need no C toolchain.
  export CGO_ENABLED=0
  BINARY="${RELEASE_SMOKE_DIR}/oberwatch"
  COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
  BUILT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "==> Building release-style binary (v${VERSION}, commit ${COMMIT})..."
  "$GO" build \
    -ldflags "-s -w -X main.version=v${VERSION} -X main.commit=${COMMIT} -X main.built=${BUILT}" \
    -o "$BINARY" ./cmd/oberwatch
fi
[ -x "$BINARY" ] || fail "oberwatch binary $BINARY is not executable"

echo "==> Checking that ${BINARY} reports v${VERSION}..."
VERSION_OUTPUT="$("$BINARY" version)"
printf '%s\n' "$VERSION_OUTPUT" | sed 's/^/  /'
FIRST_LINE="$(printf '%s\n' "$VERSION_OUTPUT" | head -n 1)"
[ "$FIRST_LINE" = "oberwatch v${VERSION}" ] \
  || fail "binary reports \"${FIRST_LINE}\"; the newest CHANGELOG.md release is ${VERSION}"
FLAG_LINE="$("$BINARY" --version | head -n 1)"
[ "$FLAG_LINE" = "oberwatch v${VERSION}" ] \
  || fail "--version reports \"${FLAG_LINE}\", want \"oberwatch v${VERSION}\""

CONTRACTS=(
  "init-validate|scripts/test-cli-config.sh"
  "runaway-kill-enable|scripts/dev/test-runaway.sh"
  "task-budgets|scripts/dev/test-task-budget.sh"
)
failed=0
for entry in "${CONTRACTS[@]}"; do
  name="${entry%%|*}"
  script="${entry#*|}"
  transcript="${RELEASE_SMOKE_DIR}/${name}.log"
  echo ""
  echo "==> Contract ${name}: ${script} ${BINARY}"
  set +e
  "$script" "$BINARY" 2>&1 | tee "$transcript"
  status=${PIPESTATUS[0]}
  set -e
  if [ "$status" -eq 0 ]; then
    echo "==> Contract ${name}: PASS"
  else
    echo "==> Contract ${name}: FAIL (exit ${status})" >&2
    failed=$((failed + 1))
  fi
done

echo ""
if [ "$failed" -ne 0 ]; then
  fail "${failed} of ${#CONTRACTS[@]} release smoke contracts failed for oberwatch v${VERSION}"
fi
echo "PASS: release smoke for oberwatch v${VERSION}: init/validate, runaway kill/enable, task budgets."
if [ "$KEEP_TRANSCRIPTS" -eq 1 ]; then
  echo "transcripts: ${RELEASE_SMOKE_DIR}"
fi
