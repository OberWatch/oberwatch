#!/usr/bin/env bash
# Exercises `oberwatch init` and `oberwatch validate` against a built binary.
#
# Covers: default output path, --output, --force and refusal without it,
# init -> validate round trip (search order and explicit paths), root and
# subcommand --config placement, missing files, malformed TOML, semantic
# validation failures, and onboarding warnings on stderr.
#
# Usage: scripts/test-cli-config.sh [path-to-oberwatch-binary]
# Without an argument the script builds ./cmd/oberwatch into a temp dir.
set -u

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/oberwatch-cli-config.XXXXXX")"
cleanup() { rm -rf "$TEMP_DIR"; }
trap cleanup EXIT

# Build with the caller's HOME so the Go module cache is reused.
BIN="${1:-}"
if [ -z "$BIN" ]; then
  BIN="$TEMP_DIR/oberwatch"
  if ! (cd "$ROOT_DIR" && go build -o "$BIN" ./cmd/oberwatch); then
    echo "FAIL: build ./cmd/oberwatch" >&2
    exit 1
  fi
fi
case "$BIN" in
  /*) ;;
  *) BIN="$(pwd)/$BIN" ;;
esac

# Keep the user's real config out of the search order.
export HOME="$TEMP_DIR/home"
mkdir -p "$HOME"

# OBERWATCH_* variables are applied as config overrides, so any left in the
# caller's environment would change what validate reports.
while IFS='=' read -r name _; do
  case "$name" in
    OBERWATCH_*) unset "$name" ;;
  esac
done < <(env)

# The system path is part of the documented search order and cannot be
# redirected, so the "no config anywhere" checks only hold without it.
SYSTEM_CONFIG=/etc/oberwatch/oberwatch.toml
HAVE_SYSTEM_CONFIG=0
if [ -e "$SYSTEM_CONFIG" ]; then
  HAVE_SYSTEM_CONFIG=1
  printf 'NOTE: %s exists; skipping the empty-search-order checks\n' "$SYSTEM_CONFIG" >&2
fi

failures=0
STDOUT="$TEMP_DIR/stdout"
STDERR="$TEMP_DIR/stderr"

pass() { printf 'PASS: %s\n' "$1"; }
fail() {
  printf 'FAIL: %s\n' "$1" >&2
  if [ -s "$STDOUT" ]; then printf '  stdout: %s\n' "$(cat "$STDOUT")" >&2; fi
  if [ -s "$STDERR" ]; then printf '  stderr: %s\n' "$(cat "$STDERR")" >&2; fi
  failures=$((failures + 1))
}

# run <workdir> <args...>: runs the binary, captures stdout/stderr and exit code.
run() {
  local dir=$1
  shift
  (cd "$dir" && "$BIN" "$@" >"$STDOUT" 2>"$STDERR")
  status=$?
}

expect_exit() {
  local description=$1 want=$2
  if [ "$status" -eq "$want" ]; then pass "$description"; else fail "$description (exit $status, want $want)"; fi
}

expect_stdout_exact() {
  local description=$1 want=$2
  if [ "$(cat "$STDOUT")" = "$want" ]; then pass "$description"; else fail "$description (want stdout: $want)"; fi
}

expect_stderr_contains() {
  local description=$1 literal=$2
  if grep -F -- "$literal" "$STDERR" >/dev/null 2>&1; then pass "$description"; else fail "$description (want stderr containing: $literal)"; fi
}

expect_stderr_empty() {
  local description=$1
  if [ ! -s "$STDERR" ]; then pass "$description"; else fail "$description (want empty stderr)"; fi
}

expect_stdout_empty() {
  local description=$1
  if [ ! -s "$STDOUT" ]; then pass "$description"; else fail "$description (want empty stdout)"; fi
}

# --- init defaults to ./oberwatch.toml and round-trips through validate -----
WORK="$TEMP_DIR/work"
mkdir -p "$WORK"

run "$WORK" init
expect_exit 'init with no flags exits 0' 0
expect_stdout_exact 'init prints the default path and serve next step' \
  "$(printf 'wrote starter config to ./oberwatch.toml\nnext: oberwatch serve --config ./oberwatch.toml')"
[ -f "$WORK/oberwatch.toml" ] && pass 'init wrote ./oberwatch.toml' || fail 'init wrote ./oberwatch.toml'
cp "$WORK/oberwatch.toml" "$TEMP_DIR/starter.toml"

run "$WORK" validate
expect_exit 'validate finds ./oberwatch.toml via search order' 0
expect_stdout_exact 'validate output is exact for search-order path' 'config ./oberwatch.toml is valid'
expect_stderr_empty 'starter config produces no warnings'

run "$WORK" validate --config oberwatch.toml
expect_exit 'validate accepts --config after the subcommand' 0
expect_stdout_exact 'validate echoes the --config path as given' 'config oberwatch.toml is valid'

run "$WORK" --config "$WORK/oberwatch.toml" validate
expect_exit 'validate accepts root --config before the subcommand' 0
expect_stdout_exact 'validate echoes the root --config path' "config $WORK/oberwatch.toml is valid"

run "$WORK" -c oberwatch.toml validate
expect_exit 'validate accepts the -c shorthand' 0

# --- init refuses to truncate without --force -------------------------------
run "$WORK" init
expect_exit 'init refuses an existing file' 1
expect_stdout_empty 'refused init prints nothing on stdout'
expect_stderr_contains 'refused init names the file' 'refusing to overwrite existing file "./oberwatch.toml"'
expect_stderr_contains 'refused init points at --force' '--force'
cmp -s "$WORK/oberwatch.toml" "$TEMP_DIR/starter.toml" && pass 'refused init left the file untouched' || fail 'refused init left the file untouched'

printf 'garbage\n' >"$WORK/oberwatch.toml"
run "$WORK" init
expect_exit 'init refuses even when the existing file is invalid' 1
[ "$(cat "$WORK/oberwatch.toml")" = "garbage" ] && pass 'refused init did not truncate the invalid file' || fail 'refused init did not truncate the invalid file'

run "$WORK" init --force
expect_exit 'init --force exits 0' 0
expect_stdout_exact 'init --force prints the path and next step' \
  "$(printf 'wrote starter config to ./oberwatch.toml\nnext: oberwatch serve --config ./oberwatch.toml')"
cmp -s "$WORK/oberwatch.toml" "$TEMP_DIR/starter.toml" && pass 'init --force rewrote the starter config' || fail 'init --force rewrote the starter config'

# --- non-default output paths ------------------------------------------------
NESTED="$TEMP_DIR/custom/deep/config.toml"
run "$WORK" init --output "$NESTED"
expect_exit 'init --output creates nested directories' 0
expect_stdout_exact 'init --output echoes the requested path' \
  "$(printf 'wrote starter config to %s\nnext: oberwatch serve --config %s' "$NESTED" "$NESTED")"
cmp -s "$NESTED" "$TEMP_DIR/starter.toml" && pass 'nested output matches the starter config' || fail 'nested output matches the starter config'

run "$WORK" validate --config "$NESTED"
expect_exit 'validate accepts the nested output path' 0
expect_stdout_exact 'validate echoes the nested path' "config $NESTED is valid"

run "$WORK" init -o relative/dir/oberwatch.toml
expect_exit 'init -o accepts a relative nested path' 0
run "$WORK" validate -c relative/dir/oberwatch.toml
expect_exit 'validate round-trips the relative nested path' 0
expect_stdout_exact 'validate echoes the relative path as given' 'config relative/dir/oberwatch.toml is valid'

run "$WORK" init --output "$TEMP_DIR/custom"
expect_exit 'init refuses to write over a directory' 1
expect_stderr_contains 'init explains the directory refusal' 'is a directory'

# A dangling symlink must not be followed: writing through it would land the
# config somewhere the user never named.
ln -s "$TEMP_DIR/symlink-target.toml" "$TEMP_DIR/dangling.toml"
run "$WORK" init --output "$TEMP_DIR/dangling.toml"
expect_exit 'init refuses a dangling symlink' 1
expect_stderr_contains 'init names the refused symlink' 'refusing to overwrite existing file'
[ ! -e "$TEMP_DIR/symlink-target.toml" ] \
  && pass 'init did not write through the dangling symlink' \
  || fail 'init did not write through the dangling symlink'

# --- errors are reported once ------------------------------------------------
run "$WORK" validate --config "$TEMP_DIR/does-not-exist.toml"
error_lines=$(grep -c -i -- 'not found' "$STDERR" || true)
[ "$error_lines" -eq 1 ] \
  && pass 'a failing command reports its error exactly once' \
  || fail "a failing command reports its error exactly once (got $error_lines lines)"

# --- validate failure modes --------------------------------------------------
EMPTY="$TEMP_DIR/empty"
mkdir -p "$EMPTY"
if [ "$HAVE_SYSTEM_CONFIG" -eq 0 ]; then
  run "$EMPTY" validate
  expect_exit 'validate with no config anywhere exits 1' 1
  expect_stdout_empty 'missing search-order config prints nothing on stdout'
  expect_stderr_contains 'missing search-order config lists the search order' 'no config file found; checked --config, ./oberwatch.toml'
  expect_stderr_contains 'missing search-order config lists the system path' '/etc/oberwatch/oberwatch.toml'
fi

run "$EMPTY" validate --config "$TEMP_DIR/does-not-exist.toml"
expect_exit 'validate with a missing --config path exits 1' 1
expect_stderr_contains 'missing --config path is named' "config file \"$TEMP_DIR/does-not-exist.toml\" not found"
expect_stderr_contains 'missing --config path reports its source' 'source: --config flag'

run "$EMPTY" --config "$TEMP_DIR/does-not-exist.toml" validate
expect_exit 'validate with a missing root --config path exits 1' 1
expect_stderr_contains 'missing root --config path is named' 'does-not-exist.toml'

printf '[server]\nport = \n' >"$TEMP_DIR/malformed.toml"
run "$EMPTY" validate --config "$TEMP_DIR/malformed.toml"
expect_exit 'malformed TOML exits 1' 1
expect_stdout_empty 'malformed TOML prints nothing on stdout'
expect_stderr_contains 'malformed TOML names the file' "parse config \"$TEMP_DIR/malformed.toml\""
expect_stderr_contains 'malformed TOML reports the line' 'line 2'

printf '[server]\nprot = 8080\n' >"$TEMP_DIR/unknown-key.toml"
run "$EMPTY" validate --config "$TEMP_DIR/unknown-key.toml"
expect_exit 'unknown key exits 1' 1
expect_stderr_contains 'unknown key is named' 'unknown key(s): server.prot'

printf '[server]\nport = 0\nhost = ""\n' >"$TEMP_DIR/semantic.toml"
run "$EMPTY" validate --config "$TEMP_DIR/semantic.toml"
expect_exit 'semantically invalid config exits 1' 1
expect_stdout_empty 'semantic failure prints nothing on stdout'
expect_stderr_contains 'semantic failure names the file' "validate config \"$TEMP_DIR/semantic.toml\""
expect_stderr_contains 'semantic failure names server.port' 'server.port must be between 1 and 65535, got 0'
expect_stderr_contains 'semantic failure names server.host' 'server.host must not be empty'

run "$EMPTY" validate --config "$TEMP_DIR/custom"
expect_exit 'validate rejects a directory path' 1
expect_stderr_contains 'directory path is explained' 'is a directory'

# --- warnings reflect session-based onboarding -------------------------------
printf '[server]\nadmin_token = "legacy"\n' >"$TEMP_DIR/legacy.toml"
run "$EMPTY" validate --config "$TEMP_DIR/legacy.toml"
expect_exit 'legacy admin_token still validates' 0
expect_stdout_exact 'legacy admin_token keeps exact stdout' "config $TEMP_DIR/legacy.toml is valid"
expect_stderr_contains 'legacy admin_token warns on stderr' 'warning: server.admin_token is set but not used'
expect_stderr_contains 'legacy admin_token warning explains session auth' 'session-based'

printf '[server]\ndashboard = false\n' >"$TEMP_DIR/no-dashboard.toml"
run "$EMPTY" validate --config "$TEMP_DIR/no-dashboard.toml"
expect_exit 'dashboard=false still validates' 0
expect_stderr_contains 'dashboard=false warns about the setup endpoint' 'warning: server.dashboard is false'
expect_stderr_contains 'dashboard=false names the setup endpoint' '/_oberwatch/api/v1/setup'

if grep -F -- 'REQUIRED in production' "$TEMP_DIR/starter.toml" >/dev/null 2>&1 \
  || grep -F -- 'management API is disabled' "$TEMP_DIR/starter.toml" >/dev/null 2>&1; then
  fail 'starter config does not restore bearer-only auth claims'
else
  pass 'starter config does not restore bearer-only auth claims'
fi
grep -F -- 'session-based' "$TEMP_DIR/starter.toml" >/dev/null 2>&1 \
  && pass 'starter config explains session-based auth' || fail 'starter config explains session-based auth'

if [ "$failures" -ne 0 ]; then
  printf '%d check(s) failed\n' "$failures" >&2
  exit 1
fi
printf 'all CLI config checks passed\n'
