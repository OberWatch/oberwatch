#!/usr/bin/env bash
# Harness for the issue #25 spike: can Oberwatch drop cgo by swapping
# github.com/mattn/go-sqlite3 for a pure Go SQLite driver?
#
# Nothing here touches the production tree. Each driver is exercised in a
# scratch copy of the repo under $SPIKE_WORK, patched to use a different
# driver adapter from harness/. Output goes to logs/.
#
# Usage:
#   ./run.sh all
#   ./run.sh env baseline deps matrix tests cross bench race
#
# Environment:
#   SPIKE_WORK   scratch directory (default ~/.cache/oberwatch-spike-25)
#   GO           go binary (default: go on PATH)
#   SPIKE_KEEP   set to 1 to reuse an already-prepared scratch tree

set -uo pipefail

SPIKE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SPIKE_DIR/../../.." && pwd)"
LOG_DIR="$SPIKE_DIR/logs"
HARNESS_DIR="$SPIKE_DIR/harness"
WORK="${SPIKE_WORK:-$HOME/.cache/oberwatch-spike-25}"
GO="${GO:-go}"

# Cold builds of the transpiled drivers write hundreds of MB of intermediate
# objects. Keep them next to the scratch trees rather than in a shared /tmp,
# which on this host is a tmpfs that other work also fills.
mkdir -p "$WORK/tmp"
export TMPDIR="$WORK/tmp"

# Pinned candidate versions. Changing these invalidates every log in logs/.
MODERNC_VERSION="v1.57.0"
NCRUCES_VERSION="v0.35.3"

DRIVERS=(mattn modernc ncruces)

mkdir -p "$LOG_DIR"

# ---------------------------------------------------------------- helpers ---

# run_timed <label> -- <cmd...> : run a command, report wall/user/sys/maxrss.
# Env for the command goes through `env VAR=...` so nothing leaks between calls.
run_timed() {
	local label="$1"
	shift
	[[ "${1:-}" == "--" ]] && shift
	echo "## $label"
	/usr/bin/time -f "wall=%es user=%Us sys=%Ss maxrss=%MKB" "$@" 2>&1
	echo "exit=$?"
}

# cgo_env <driver> : CGO_ENABLED value the driver needs.
cgo_env() {
	if [[ "$1" == "mattn" ]]; then echo 1; else echo 0; fi
}

# magic <file> : first four bytes in hex (7f454c46 = ELF, cffaedfe = Mach-O).
magic() {
	od -An -tx1 -N4 "$1" | tr -d ' \n'
}

# copy_repo <dest> : working-tree copy of the repo without VCS/build junk.
copy_repo() {
	local dest="$1"
	rm -rf "$dest"
	mkdir -p "$dest"
	rsync -a \
		--exclude '.git' \
		--exclude 'bin/' \
		--exclude '.tools/' \
		--exclude 'tmp/' \
		--exclude 'node_modules/' \
		--exclude 'dashboard/svelte/build/' \
		--exclude 'dashboard/svelte/.svelte-kit/' \
		--exclude 'docs/spikes/' \
		"$REPO_ROOT/" "$dest/"
}

# patch_storage <dest> : move the driver-coupled lines out of sqlite.go so a
# driver adapter from harness/ can supply them. This is the shape the real
# port would take, so the spike measures the real change.
patch_storage() {
	python3 - "$1/internal/storage/sqlite.go" <<'PY'
import sys

path = sys.argv[1]
src = open(path).read()

edits = [
    # Drop the mattn import; the adapter file owns the driver import now.
    ('\t"github.com/OberWatch/oberwatch/internal/config"\n'
     '\t// Register SQLite driver with database/sql.\n'
     '\tsqlite3 "github.com/mattn/go-sqlite3"\n)',
     '\t"github.com/OberWatch/oberwatch/internal/config"\n)'),
    # errors is only used by isSQLiteConstraint, which moves to the adapter.
    ('\t"errors"\n', ''),
    # The driver name is no longer a literal.
    ('sql.Open("sqlite3", dsn)', 'sql.Open(sqliteDriverName, dsn)'),
    # isSQLiteConstraint moves to the adapter.
    ('func isSQLiteConstraint(err error) bool {\n'
     '\tvar sqliteErr sqlite3.Error\n'
     '\treturn errors.As(err, &sqliteErr) && sqliteErr.Code == sqlite3.ErrConstraint\n'
     '}\n\n', ''),
]

for old, new in edits:
    if old not in src:
        sys.exit("patch_storage: pattern not found in sqlite.go:\n%r" % old)
    src = src.replace(old, new, 1)

open(path, "w").write(src)
PY
}

# install_adapter <dest> <driver> : copy the adapter and the spike tests in,
# stripping their //go:build ignore guards.
install_adapter() {
	local dest="$1" driver="$2"
	sed '/^\/\/go:build ignore$/d' "$HARNESS_DIR/driver_$driver.go" \
		>"$dest/internal/storage/driver_spike.go"
	sed '/^\/\/go:build ignore$/d' "$HARNESS_DIR/spike_driver_test.go" \
		>"$dest/internal/storage/spike_driver_test.go"
}

# prepare <driver> : build a ready-to-test scratch tree at $WORK/<driver>.
prepare() {
	local driver="$1"
	local dest="$WORK/$driver"
	if [[ "${SPIKE_KEEP:-0}" == "1" && -f "$dest/.spike-ready" ]]; then
		return 0
	fi
	copy_repo "$dest" || return 1
	install_adapter "$dest" "$driver" || return 1
	# Every driver, including the mattn baseline, gets the same extracted
	# adapter shape so the comparison is like for like.
	patch_storage "$dest" || return 1
	case "$driver" in
	mattn) ;;
	modernc)
		(cd "$dest" && $GO get "modernc.org/sqlite@$MODERNC_VERSION") >/dev/null 2>&1 || return 1
		;;
	ncruces)
		(cd "$dest" && $GO get "github.com/ncruces/go-sqlite3@$NCRUCES_VERSION") >/dev/null 2>&1 || return 1
		;;
	esac
	(cd "$dest" && CGO_ENABLED="$(cgo_env "$driver")" $GO mod tidy) >/dev/null 2>&1 || {
		echo "prepare($driver): go mod tidy failed" >&2
		return 1
	}
	touch "$dest/.spike-ready"
}

# ----------------------------------------------------------------- stages ---

stage_env() {
	local log="$LOG_DIR/00-environment.log"
	{
		echo "# spike environment"
		echo "date_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
		echo "go=$($GO version)"
		echo "goroot=$($GO env GOROOT)"
		echo "host=$(uname -srm)"
		echo "cpus=$(nproc)"
		echo "mem_total_kb=$(awk '/MemTotal/{print $2}' /proc/meminfo)"
		echo "cc=$(gcc --version 2>/dev/null | head -1)"
		echo "clang=$(clang --version 2>/dev/null | head -1 || echo 'not installed')"
		echo "repo_head=$(git -C "$REPO_ROOT" rev-parse HEAD)"
		echo "repo_branch=$(git -C "$REPO_ROOT" rev-parse --abbrev-ref HEAD)"
		echo "pinned_modernc=$MODERNC_VERSION"
		echo "pinned_ncruces=$NCRUCES_VERSION"
		echo "work_dir=$WORK"
	} >"$log" 2>&1
	echo "wrote $log"
}

# Prepare every scratch tree and report whether it compiles. Useful on its own
# when changing the adapters; the other stages call prepare themselves.
stage_prepare() {
	local driver
	for driver in "${DRIVERS[@]}"; do
		if prepare "$driver"; then
			(
				cd "$WORK/$driver" || exit 1
				export CGO_ENABLED="$(cgo_env "$driver")"
				$GO build ./... 2>&1 && $GO vet ./internal/storage/ 2>&1
				echo "  $driver: build+vet exit=$?"
			)
		else
			echo "  $driver: prepare failed"
		fi
	done
}

# 01/02: the mattn baseline every pure Go result is compared against.
stage_baseline() {
	prepare mattn || return 1
	local build_log="$LOG_DIR/01-baseline-mattn-builds.log"
	local test_log="$LOG_DIR/02-baseline-mattn-tests.log"
	local out="$WORK/artifacts"
	mkdir -p "$out"

	(
		cd "$WORK/mattn" || exit 1
		echo "# baseline: mattn/go-sqlite3, $($GO version), host $(uname -srm), $(nproc) cpus"
		echo
		$GO clean -cache >/dev/null 2>&1
		run_timed "linux/amd64 CGO_ENABLED=1 cold build (-a)" -- \
			env CGO_ENABLED=1 "$GO" build -a -o "$out/ob-mattn-linux-amd64" ./cmd/oberwatch
		touch cmd/oberwatch/main.go
		run_timed "linux/amd64 CGO_ENABLED=1 warm rebuild (touch main)" -- \
			env CGO_ENABLED=1 "$GO" build -o "$out/ob-mattn-linux-amd64" ./cmd/oberwatch
		echo "size_bytes=$(stat -c %s "$out/ob-mattn-linux-amd64") magic=$(magic "$out/ob-mattn-linux-amd64")"
		run_timed "linux/amd64 release-style build (-ldflags '-s -w', as GoReleaser builds it)" -- \
			env CGO_ENABLED=1 "$GO" build -ldflags="-s -w" -o "$out/rel-mattn-linux-amd64" ./cmd/oberwatch
		echo "release_size_bytes=$(stat -c %s "$out/rel-mattn-linux-amd64")"
		echo "## ldd:"
		ldd "$out/ob-mattn-linux-amd64" 2>&1
		echo
		for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
			echo "## $target CGO_ENABLED=0"
			env GOOS="${target%/*}" GOARCH="${target#*/}" CGO_ENABLED=0 \
				"$GO" build -o /dev/null ./cmd/oberwatch 2>&1
			echo "exit=$?"
		done
		echo "## darwin/arm64 CGO_ENABLED=1 (needs a darwin cross toolchain)"
		env GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 "$GO" build -o /dev/null ./cmd/oberwatch 2>&1
		echo "exit=$?"
	) >"$build_log" 2>&1
	echo "wrote $build_log"

	(
		cd "$WORK/mattn" || exit 1
		run_timed "go test ./internal/storage/... -count=1 -skip TestSpike (mattn, CGO=1)" -- \
			env CGO_ENABLED=1 "$GO" test ./internal/storage/... -count=1 -skip 'TestSpike'
		echo
		run_timed "go test -race ./internal/storage/... -count=1 -skip TestSpike" -- \
			env CGO_ENABLED=1 "$GO" test -race ./internal/storage/... -count=1 -skip 'TestSpike'
		echo
		run_timed "existing suite: go test ./... -count=1 -skip TestSpike" -- \
			env CGO_ENABLED=1 "$GO" test ./... -count=1 -skip 'TestSpike'
		echo
		echo "## go vet ./..."
		env CGO_ENABLED=1 "$GO" vet ./... 2>&1
		echo "exit=$?"
	) >"$test_log" 2>&1
	echo "wrote $test_log"
}

# 03: dependency surface, licenses and known vulnerabilities per driver.
stage_deps() {
	local log="$LOG_DIR/03-deps-licenses-vulns.log"
	local tools="$WORK/tools"
	mkdir -p "$tools"
	GOBIN="$tools/bin" $GO install golang.org/x/vuln/cmd/govulncheck@latest >/dev/null 2>&1
	GOBIN="$tools/bin" $GO install github.com/google/go-licenses@latest >/dev/null 2>&1

	: >"$log"
	for driver in "${DRIVERS[@]}"; do
		prepare "$driver" || continue
		(
			cd "$WORK/$driver" || exit 1
			export CGO_ENABLED="$(cgo_env "$driver")"
			echo "########## $driver"
			echo "## go.mod require block"
			sed -n '/^require/,/^)/p' go.mod
			echo "## module count (go list -m all): $($GO list -m all 2>/dev/null | wc -l)"
			echo "## sqlite-related modules:"
			$GO list -m all 2>/dev/null |
				grep -Ei 'sqlite|ncruces|modernc' | grep -v '^github.com/OberWatch'
			echo "## govulncheck ./..."
			if [[ -x "$tools/bin/govulncheck" ]]; then
				"$tools/bin/govulncheck" ./... >"$WORK/vuln-$driver.txt" 2>&1
				echo "### findings (id / module / fixed in)"
				grep -E '^Vulnerability #|^ *Found in: |^ *Fixed in: |^  Standard library$' \
					"$WORK/vuln-$driver.txt" | sed 's/^ *//' | paste - - - -
				echo "### summary"
				sed -n '/Your code is affected by/,$p' "$WORK/vuln-$driver.txt"
				echo "### attributable to the sqlite driver tree:"
				grep -E 'Found in: (modernc|github.com/ncruces|github.com/mattn)' \
					"$WORK/vuln-$driver.txt" || echo "none"
			else
				echo "govulncheck unavailable"
			fi
			echo "## licenses reachable from internal/storage:"
			if [[ -x "$tools/bin/go-licenses" ]]; then
				"$tools/bin/go-licenses" report ./internal/storage 2>/dev/null |
					grep -Ei 'sqlite|ncruces|modernc'
			else
				echo "go-licenses unavailable"
			fi
			echo
		) >>"$log" 2>&1
	done
	echo "wrote $log"
}

# 04: does a pure Go build actually cross-compile everywhere, at what cost?
stage_matrix() {
	local log="$LOG_DIR/04-pure-go-build-matrix.log"
	local out="$WORK/artifacts"
	mkdir -p "$out"
	: >"$log"

	for driver in modernc ncruces; do
		prepare "$driver" || continue
		(
			cd "$WORK/$driver" || exit 1
			export CGO_ENABLED=0
			echo "########## $driver (CGO_ENABLED=0)"
			$GO clean -cache >/dev/null 2>&1
			run_timed "linux/amd64 cold build (-a)" -- \
				"$GO" build -a -o "$out/ob-$driver-linux-amd64" ./cmd/oberwatch
			touch cmd/oberwatch/main.go
			run_timed "linux/amd64 warm rebuild (touch main)" -- \
				"$GO" build -o "$out/ob-$driver-linux-amd64" ./cmd/oberwatch
			$GO clean -cache >/dev/null 2>&1
			run_timed "linux/amd64 cold build of ./internal/storage only" -- \
				"$GO" build ./internal/storage/
			for target in linux/arm64 darwin/amd64 darwin/arm64; do
				run_timed "$target" -- \
					env GOOS="${target%/*}" GOARCH="${target#*/}" CGO_ENABLED=0 \
					"$GO" build -o "$out/ob-$driver-${target%/*}-${target#*/}" ./cmd/oberwatch
			done
			echo "## artifacts (magic 7f454c46=ELF, cffaedfe=Mach-O 64)"
			for f in "$out/ob-$driver"-*; do
				printf '%-32s %10s bytes  magic=%s\n' \
					"$(basename "$f")" "$(stat -c %s "$f")" "$(magic "$f")"
			done
			echo "## release-style sizes (-ldflags '-s -w', as GoReleaser builds them)"
			for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
				env GOOS="${target%/*}" GOARCH="${target#*/}" CGO_ENABLED=0 \
					"$GO" build -ldflags="-s -w" \
					-o "$out/rel-$driver-${target%/*}-${target#*/}" ./cmd/oberwatch
				printf '%-32s %10s bytes\n' "rel-$driver-${target%/*}-${target#*/}" \
					"$(stat -c %s "$out/rel-$driver-${target%/*}-${target#*/}")"
			done
			echo "## ldd linux/amd64:"
			ldd "$out/ob-$driver-linux-amd64" 2>&1
			echo "## go version -m (driver + build settings):"
			$GO version -m "$out/ob-$driver-linux-amd64" |
				grep -E 'dep[[:space:]]+(modernc|github.com/ncruces|github.com/mattn)|CGO_ENABLED|GOARCH|GOOS'
			echo
		) >>"$log" 2>&1
	done

	(
		echo "########## mattn artifact for comparison"
		if [[ -f "$out/ob-mattn-linux-amd64" ]]; then
			echo "ob-mattn-linux-amd64  $(stat -c %s "$out/ob-mattn-linux-amd64") bytes  magic=$(magic "$out/ob-mattn-linux-amd64")"
			[[ -f "$out/rel-mattn-linux-amd64" ]] &&
				echo "rel-mattn-linux-amd64 $(stat -c %s "$out/rel-mattn-linux-amd64") bytes (-ldflags '-s -w')"
			$GO version -m "$out/ob-mattn-linux-amd64" |
				grep -E 'dep[[:space:]]+github.com/mattn|CGO_ENABLED'
		else
			echo "run ./run.sh baseline first"
		fi
	) >>"$log" 2>&1
	echo "wrote $log"
}

# 10: behaviour. Spike harness plus the real suite, per driver.
stage_tests() {
	local driver log
	for driver in "${DRIVERS[@]}"; do
		prepare "$driver" || continue
		log="$LOG_DIR/10-$driver-tests.log"
		(
			cd "$WORK/$driver" || exit 1
			export CGO_ENABLED="$(cgo_env "$driver")"
			export SPIKE_FIXTURE_OUT="$WORK/fixtures/$driver"
			rm -rf "$SPIKE_FIXTURE_OUT"
			echo "# $driver  CGO_ENABLED=$CGO_ENABLED  $($GO version)"
			run_timed "harness: go test -run TestSpike -v ./internal/storage/ (writes fixtures)" -- \
				"$GO" test -count=1 -run 'TestSpike' -v ./internal/storage/
			echo
			run_timed "existing suite: go test -count=1 -skip TestSpike ./..." -- \
				"$GO" test -count=1 -skip 'TestSpike' ./...
			echo
			echo "## go vet ./..."
			$GO vet ./... 2>&1
			echo "exit=$?"
		) >"$log" 2>&1
		echo "wrote $log"
	done
}

# 11: on-disk compatibility. Every driver must open every other driver's files.
stage_cross() {
	local log="$LOG_DIR/11-cross-driver-fixtures.log"
	: >"$log"
	local writer reader
	for writer in "${DRIVERS[@]}"; do
		if [[ ! -d "$WORK/fixtures/$writer" ]]; then
			echo "## no fixtures for $writer; run ./run.sh tests first" >>"$log"
			continue
		fi
		for reader in "${DRIVERS[@]}"; do
			[[ "$writer" == "$reader" ]] && continue
			(
				cd "$WORK/$reader" || exit 1
				echo "## fixtures written by $writer, opened by $reader"
				env CGO_ENABLED="$(cgo_env "$reader")" \
					SPIKE_FIXTURE_IN="$WORK/fixtures/$writer" \
					"$GO" test -count=1 -run 'TestSpike_OpenFixture' -v ./internal/storage/ 2>&1 |
					grep -vE '^(=== RUN|--- SKIP)'
			) >>"$log" 2>&1
		done
	done
	echo "wrote $log"
}

# 12: throughput and latency on the store's real query paths.
stage_bench() {
	local log="$LOG_DIR/12-benchmarks.log"
	: >"$log"
	local driver
	for driver in "${DRIVERS[@]}"; do
		prepare "$driver" || continue
		(
			cd "$WORK/$driver" || exit 1
			export CGO_ENABLED="$(cgo_env "$driver")"
			echo "########## $driver (CGO_ENABLED=$CGO_ENABLED)"
			run_timed "go test -run '^\$' -bench BenchmarkSpike -benchmem -benchtime 2s -count=3" -- \
				"$GO" test -run '^$' -bench 'BenchmarkSpike' -benchmem -benchtime 2s -count=3 \
				./internal/storage/
			echo
		) >>"$log" 2>&1
		echo "  benched $driver"
	done
	echo "wrote $log"
}

# 13: race detector over the spike harness and the real suite.
stage_race() {
	local log="$LOG_DIR/13-race.log"
	: >"$log"
	local driver
	for driver in "${DRIVERS[@]}"; do
		prepare "$driver" || continue
		(
			cd "$WORK/$driver" || exit 1
			# -race needs cgo, so the pure Go drivers are compiled with
			# CGO_ENABLED=1 here. That applies to the test binary only; the
			# shipped binary is still the CGO_ENABLED=0 build from stage 04.
			echo "########## $driver (CGO_ENABLED=1, required by -race)"
			run_timed "go test -race -count=1 -run TestSpike ./internal/storage/" -- \
				env CGO_ENABLED=1 "$GO" test -race -count=1 -run 'TestSpike' ./internal/storage/
			echo
			run_timed "existing suite: go test -race -count=1 -skip TestSpike ./..." -- \
				env CGO_ENABLED=1 "$GO" test -race -count=1 -skip 'TestSpike' ./...
			echo
		) >>"$log" 2>&1
		echo "  raced $driver"
	done
	echo "wrote $log"
}

# ------------------------------------------------------------------- main ---

stages=("$@")
[[ ${#stages[@]} -eq 0 ]] && stages=(all)
[[ "${stages[0]}" == "all" ]] && stages=(env baseline deps matrix tests cross bench race)

for stage in "${stages[@]}"; do
	echo "=== stage: $stage ==="
	"stage_$stage" || echo "stage $stage reported a failure" >&2
done
