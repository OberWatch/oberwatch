# Decision: pure Go SQLite driver (issue #25)

**Status:** approved.

**Migrate to `modernc.org/sqlite v1.57.0`.** Drop `github.com/mattn/go-sqlite3
v1.14.22` and build every shipped artifact with `CGO_ENABLED=0`.

The migration itself is issue #26. Its contract is at the end of this file.

Every number below comes from `logs/` on this branch. Stage numbers in
parentheses point at the file that produced them. Everything was measured on
one host (`logs/00-environment.log`): go1.26.0 linux/amd64, 8 CPUs, AMD
EPYC-Rome, 15.6 GB RAM, gcc 15.2.0, no clang installed.

## The problem, restated with evidence

`github.com/mattn/go-sqlite3` is the only cgo dependency in the tree. It is
imported in exactly one production file, `internal/storage/sqlite.go`, at three
sites: the import, `sql.Open("sqlite3", dsn)` (line 41), and
`isSQLiteConstraint` (lines 1147-1148).

Those three sites cost the project:

- **No `CGO_ENABLED=0` build at all.** Not a runtime failure, a compile failure.
  On the current tree, `CGO_ENABLED=0 go build ./cmd/oberwatch` fails with
  `internal/storage/sqlite.go:1147:24: undefined: sqlite3.Error`. The same
  failure appears for linux/amd64, linux/arm64, darwin/amd64 and darwin/arm64
  (01).
- **No darwin build, at all.** `GOOS=darwin GOARCH=arm64 CGO_ENABLED=1` fails
  with `cgo: C compiler "clang" not found` (01). A darwin release would need a
  macOS runner or an osxcross toolchain.
- **A dynamically linked binary.** `ldd` on the mattn build reports
  `libc.so.6` (01). The pure Go builds report `not a dynamic executable` (04).
- **A cross-compiler in the release job.** `.github/workflows/release.yml`
  installs `gcc-aarch64-linux-gnu`; `.goreleaser.yml` pins `CC=gcc` and
  `CC=aarch64-linux-gnu-gcc`; the `Dockerfile` installs `gcc musl-dev`.
- **A 57 second cold build** (01), against 29s for modernc and 23s for ncruces
  (04).

## Candidates

| | mattn (baseline) | **modernc** | ncruces |
| --- | --- | --- | --- |
| Version | v1.14.22 | **v1.57.0** | v0.35.3 |
| Implementation | cgo, C amalgamation | C transpiled to Go | SQLite as WASM on wazero |
| SQLite version (10) | 3.45.1 | **3.53.3** | 3.53.4 |
| `CGO_ENABLED=0` build | fails (01) | **all 4 targets (04)** | all 4 targets (04) |
| Static binary | no, links libc | **yes** | yes |
| Release size, linux/amd64 | 10,878,672 B | **13,123,746 B** | 15,696,034 B |
| Cold build, linux/amd64 | 57.31s | **28.84s** | 23.22s |
| Cold build peak RSS | 438 MB | **735 MB** | 1,338 MB |
| Modules (`go list -m all`) | 15 | **38** | 26 |
| Driver-attributable vulns (03) | none | **none** | none |
| Harness result (10) | pass | **pass** | pass |
| `-race` (13) | clean | **clean** | clean |

All three drivers pass the same harness with no behavioural differences worth
recording: v1→v4, v2→v4 and v3→v4 migrations, `journal_mode=wal`,
`synchronous=1`, `busy_timeout=5000`, WAL checkpoint and sidecar removal on
`Close`, cross-connection busy-timeout semantics, mixed reader/writer
concurrency, context cancellation, the full existing suite, and `go vet` (10).

### Why modernc over ncruces

ncruces is a credible driver and won two of the measurements. It builds fastest
(23.22s vs 28.84s), it holds up better under contended writes (median 187,592
ns/op vs modernc's 241,528), and it pulls 12 fewer modules. Its WASM design also
confines SQLite to a wazero linear memory, which is a stronger isolation
property than transpiled C reaching into `modernc.org/libc`. That is an
architectural observation, not something this spike measured.

modernc wins on the things that are harder to undo:

- **Smaller artifacts on every target.** 13.12 MB vs 15.70 MB stripped on
  linux/amd64, and the same 2.4-2.6 MB gap on the other three (04). Against
  mattn, modernc costs +2,245,074 bytes (+20.6%); ncruces costs +4,817,362
  (+44.3%).
- **Lower build memory.** 735 MB peak RSS vs 1,338 MB (04). Cold builds happen
  on CI runners, and 1.3 GB for one package is a constraint worth avoiding.
- **Cheaper reads.** `QueryCosts10k` allocates 110,047 times per op under
  modernc against 150,047 under ncruces, and 6.51 MB against 7.47 MB (12). The
  20k-row memory test agrees: 31,005 KB total allocated against 41,784 KB (10).
- **A settled version line.** v1.57.0 against v0.35.3.
- **No forced dependency bumps.** The ncruces tree's `go mod tidy` raised
  `golang.org/x/crypto` from v0.45.0 to v0.54.0 (03). Benign here, but it is a
  change to a security-sensitive module that the driver choice dragged in.

modernc's weakness is contended writes, and the next section bounds it.

## Tradeoffs

### Compatibility

**On-disk format: no change, verified in both directions.** Every writer/reader
pair among the three drivers opens the other's v1, v2, v3 and v4 database files,
migrates them to version 4, reads all 100 seeded cost rows, and returns
`PRAGMA integrity_check = ok`. All six pairs, all four schema versions (11).
Databases written by modernc open under mattn, which is what makes rollback
cheap.

**API and schema: no change.** The spike ran the unmodified existing suite
against each driver. `go test -count=1 -skip TestSpike ./...` is green for all
three, including `TestSQLiteStore_InvalidDSNAndNilWriterSafety`, which asserts
that passing a directory as the DSN fails, and
`TestSQLiteStore_AgentDefaultsAndRenameConflict`, which is the existing
regression test over `isSQLiteConstraint` (10).

**Two real differences.**

1. **The driver name changes.** modernc registers `sqlite`, not `sqlite3`.
   `sql.Open("sqlite3", dsn)` becomes `sql.Open("sqlite", dsn)`. Silent if
   missed: `sql.Open` does not fail until first use.

2. **Constraint errors carry extended result codes.** mattn splits `Code`
   (primary) from `ExtendedCode`, so `sqliteErr.Code == sqlite3.ErrConstraint`
   works. modernc returns a single value, and it is the extended code. Both
   `settings.key` and `agents.name` are `TEXT PRIMARY KEY`, so the violation
   reports 1555 (`SQLITE_CONSTRAINT_PRIMARYKEY`), not 2067
   (`SQLITE_CONSTRAINT_UNIQUE`) (10). Both mask to 19. The check must mask the
   low byte rather than match any single extended code. modernc's error is also
   a pointer type, `*sqlite.Error`; `errors.As` against a value type never
   matches and fails open.

Error text changes too, and it is user-visible in logs and API responses:

| | mattn | modernc |
| --- | --- | --- |
| Duplicate key | `UNIQUE constraint failed: settings.key` | `constraint failed: UNIQUE constraint failed: settings.key (1555)` |
| Missing table | `no such table: t` | `SQL logic error: no such table: t` |
| Busy | `database is locked` | `database is locked (5) (SQLITE_BUSY)` |

No production code matches on SQLite error strings, so nothing breaks. It is a
release-note item, not a code item.

### Concurrency

`NewSQLiteStore` pins the pool to a single connection
(`SetMaxOpenConns(1)`), so all database access is already serialized in-process.
The concurrency question is therefore about cross-process locking, and the
answer is unchanged: with two stores open on one file, the second writer waits
430ms for a 400ms lock hold and succeeds, then errors after 4.71s when the lock
is held past the 5s `busy_timeout` (10). mattn measures 429ms and 5.02s on the
same test. Eight concurrent writers, four concurrent readers and a retention
cleanup loop run clean, and `RenameAgent` still maps a conflict to
`ErrAgentExists` (10).

`go test -race` is clean for all three drivers, over both the harness and the
existing suite (13). The race detector needs cgo, so those runs used
`CGO_ENABLED=1`; that applies to the test binary only. CI's
`go test -race ./...` gets slower by about a second overall: the storage package
goes from 1.205s to 2.745s and the whole suite from 28.64s to 29.82s (13).

### Performance

Medians of three runs at `-benchtime 2s` (12). The host was not quiet, and the
spread inside a single driver is often as large as the gap between drivers.

| Benchmark | mattn | modernc | change |
| --- | --- | --- | --- |
| `SaveCostRecord` | 120,994 ns/op | 160,693 | +33% |
| `SaveCostRecordParallel` | 136,629 ns/op | 241,528 | +77% |
| `QueryCosts10k` | 66.09 ms/op | 82.69 | +25% |
| `UpsertAgent` | 142,018 ns/op | 127,960 | **-10%** |
| `OpenAndMigrate` | 20.74 ms/op | 13.89 | **-33%** |

Writes get slower; opens and upserts get faster. Read the absolute numbers, not
the percentages. An insert goes from ~121 µs to ~161 µs: 40 µs more per cost
record. Under contention the worst case is ~242 µs, or about 4,100 writes per
second through the single connection. Oberwatch writes one cost record per proxied
LLM call, and those calls take hundreds of milliseconds. 40 µs against that is
not measurable end to end.

`QueryCosts10k` needs care before it is read as a regression: modernc's fastest
run (53.9 ms) beat mattn's fastest (63.3 ms), while its slowest (92.6 ms) was
well behind. The medians differ by 25%; the noise is wider than that. Treat the
query path as "no clear difference at this sample size" rather than "25% slower".

Allocation counts move in modernc's favour on writes: 816 B and 17 allocs per
insert against mattn's 1,576 B and 22 (12).

### Security

`govulncheck ./...` reports the same 19 standard-library findings in all three
trees, all of them from the pinned go1.26.0 toolchain being behind go1.26.6
(03). **Zero findings are attributable to any SQLite driver tree**, in any of
the three trees. The stdlib findings are a toolchain-pinning matter and are out
of scope for this decision.

What changes:

- **Dependency surface grows from 15 modules to 38** (+23). That is 23 more
  modules for the weekly `govulncheck` job and for supply-chain review.
- **Static binaries.** No `libc.so.6` link (04), so no host-libc mismatch and
  no `sqlite-libs` needed in the runtime image.
- **The C compiler leaves the build.** Removing cgo removes a C toolchain from
  the release path. The memory-safety benefit is real but partial and was not
  measured: modernc is transpiled C running on `modernc.org/libc`, which uses
  unsafe pointer arithmetic throughout. This is not a claim that the SQLite
  logic becomes memory-safe.
- **A newer SQLite ships:** 3.53.3 against 3.45.1 (10).

### License

mattn is MIT throughout. `go-licenses report ./internal/storage` on the modernc
tree returns (03):

| Module | License |
| --- | --- |
| `modernc.org/sqlite` | BSD-3-Clause |
| `modernc.org/memory` | BSD-3-Clause |
| `modernc.org/libc` | MIT, resolved through `LICENSE-3RD-PARTY.md` |
| `modernc.org/mathutil` | **Unknown** |

Two items need a human before #26 merges. `modernc.org/mathutil` reports
Unknown, and the `modernc.org/libc` result points at a third-party license file
rather than the module's own. Neither is BSD/MIT-incompatible on its face, and
both are permissive-family projects, but "the tool could not classify it" is not
a license review. See the gates below.

For reference, ncruces has the same shape of problem:
`github.com/ncruces/go-sqlite3-wasm/v3` also reports Unknown (03).

## Build targets

After the swap, one linux/amd64 host with no C compiler produces all four
targets, `CGO_ENABLED=0`, all `exit=0` (04):

| Target | Cold build | Format | Release size |
| --- | --- | --- | --- |
| linux/amd64 | 28.84s | ELF (`7f454c46`) | 13,123,746 B |
| linux/arm64 | 23.14s | ELF | 12,517,538 B |
| darwin/amd64 | 21.85s | Mach-O 64 (`cffaedfe`) | 13,364,672 B |
| darwin/arm64 | 20.37s | Mach-O 64 | 12,782,962 B |

`go version -m` on the linux/amd64 artifact confirms `CGO_ENABLED=0` and
`modernc.org/sqlite v1.57.0` (04).

**The darwin binaries were never executed.** They are Mach-O files produced by a
cross-compile, which proves the toolchain works and nothing about whether they
run. `scripts/install.sh` still fails on macOS with "macOS binaries are not yet
available", and that stays true. Shipping darwin is a separate decision that
needs a macOS runner in CI first.

**Ship targets stay linux/amd64 and linux/arm64.** The change is that they no
longer need a cross-compiler.

## Release impact

Files that change in #26, none of them application code:

- **`.goreleaser.yml`** — both build entries drop `CGO_ENABLED=1` and their `CC`
  lines and set `CGO_ENABLED=0`. The two entries can collapse into one with
  `goarch: [amd64, arm64]`, since nothing is arch-specific any more.
- **`.github/workflows/release.yml`** — the "Install cross-compilers" step
  (`apt-get install -y gcc-aarch64-linux-gnu`) is removed.
- **`Dockerfile`** — `CGO_ENABLED=1` becomes `0`; `gcc musl-dev` leave the
  builder stage; `sqlite-libs` leaves the runtime stage, since the binary is
  statically linked (04).
- **`go.mod` / `go.sum`** — one require line swapped; the indirect block grows
  from 2 entries to 11, and the full module graph from 15 to 38 (03).

User-visible in the release notes:

- **Release tarballs grow by about 2.2 MB per binary** (+20.6%). Docker layers
  grow by the same amount.
- **The database file does not change.** Upgrading and downgrading across this
  release needs no data step, in either direction (11).
- **SQLite goes from 3.45.1 to 3.53.3**, because the driver vendors its own
  copy. No schema or query in this repo depends on anything version-specific.
- **SQLite error text changes**, per the table above. Anyone grepping logs or
  parsing API error strings for `UNIQUE constraint failed` should read the new
  wording first.

Not in this release: darwin artifacts, a smaller Docker base image, any change
to `sqlite_path` handling or DSN syntax.

## Rollback

The change is one package plus build configuration, so rollback is a revert of
the #26 pull request. No data migration in either direction.

- **Data is safe.** Databases written by modernc open under mattn: all four
  schema versions, `integrity_check = ok`, all rows present (11). A user who
  downgrades to a pre-#26 binary keeps their data.
- **Deployed binaries** roll back by reinstalling the previous release through
  `scripts/install.sh` or by pinning the previous Docker tag. No `/data` step.
- **WAL sidecars** are checkpointed and removed by `Close` on every driver
  tested (10), so a clean shutdown leaves one portable file. A process killed
  mid-write leaves a `-wal` file, and both drivers read the same WAL format, so
  that case is safe too.
- **Scope of the guarantee.** Rollback compatibility is asserted for this
  schema at these two versions (3.53.3 writing, 3.45.1 reading). It is not a
  general claim that any future SQLite 3.53 feature will be readable by 3.45. If
  a later migration starts using a newer SQLite feature, that migration has to
  re-establish this.

## Gates before #26 merges

1. **Confirm `modernc.org/mathutil` and `modernc.org/libc` licensing by hand.**
   `go-licenses` returned Unknown for the first and a third-party file for the
   second (03). Record the finding in the #26 pull request. If either is not
   permissive and BSD/MIT-compatible, #26 stops and this decision is reopened.
2. **Keep `-race` in CI on `CGO_ENABLED=1`.** The race detector needs cgo. Only
   the shipped artifacts move to `CGO_ENABLED=0`.

Neither gate blocks starting #26; both block merging it.

## Issue #26 contract

Scope is fixed by this document. Anything not listed as in-scope is out of scope
and belongs in its own issue.

### In scope

**`internal/storage/sqlite.go`** — three edits, no other change to the file:

1. Replace the `github.com/mattn/go-sqlite3` import with
   `sqlite "modernc.org/sqlite"`. The import registers the driver in its `init`
   and supplies the error type used by edit 3, so it is a named import, not a
   blank one.
2. `sql.Open("sqlite3", dsn)` → `sql.Open("sqlite", dsn)`.
3. Rewrite `isSQLiteConstraint` to mask the extended result code:

   ```go
   // SQLITE_CONSTRAINT. modernc returns the extended result code, so a primary
   // key violation arrives as 1555 and a unique violation as 2067. Mask the low
   // byte instead of matching either one.
   const sqliteConstraint = 19

   func isSQLiteConstraint(err error) bool {
       var sqliteErr *sqlite.Error
       return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqliteConstraint
   }
   ```

   The pointer receiver matters: `errors.As` against a value type silently never
   matches, and `RenameAgent` would stop returning `ErrAgentExists`.

**`go.mod` / `go.sum`** — add `modernc.org/sqlite v1.57.0`, remove
`github.com/mattn/go-sqlite3`, `go mod tidy`. The version is pinned by this
decision; a different version means re-running `./run.sh` before merging.

**Build configuration** — `.goreleaser.yml`, `.github/workflows/release.yml` and
`Dockerfile`, exactly the edits listed under Release impact.

**Tests** — three additions to the real suite, ported from `harness/`:

- v1→v4 and v2→v4 migration coverage. `TestSQLiteStore_TaskBudgetsMigration`
  already covers v3→v4; the older paths are untested in the production suite and
  the spike exercised them.
- A constraint-mapping test that asserts the masked code path, not just
  `ErrAgentExists`. `TestSQLiteStore_AgentDefaultsAndRenameConflict` passes even
  if `isSQLiteConstraint` is subtly wrong in a way that happens to fail closed.
- A pragma assertion that `journal_mode=wal`, `synchronous=1` and
  `busy_timeout=5000` are actually applied after `NewSQLiteStore`.

**`CHANGELOG.md`** — the binary size increase, the SQLite version bump, the
error-text change, and an explicit statement that the database file is unchanged.

### Out of scope

- Any schema change, any migration beyond version 4.
- Any change to `SetMaxOpenConns(1)` or the pool configuration.
- darwin release artifacts, and any change to `scripts/install.sh`.
- Support for DSN query parameters or `file:` URIs. `sqlite_path` stays a plain
  filesystem path.
- Docker base image changes beyond removing `gcc`, `musl-dev` and `sqlite-libs`.
- Performance work aimed at closing the write-throughput gap.
- Migrating to any other driver, including ncruces.

### Acceptance criteria

1. `CGO_ENABLED=0 go build ./cmd/oberwatch` succeeds for linux/amd64,
   linux/arm64, darwin/amd64 and darwin/arm64.
2. `ldd` on the linux/amd64 artifact reports `not a dynamic executable`.
3. `go test ./... -count=1` and `go test -race ./... -count=1` are green, with
   coverage still at or above the 80% CI threshold.
4. `go vet ./...` and `golangci-lint run` are clean.
5. `grep -r mattn` returns nothing outside `docs/spikes/`.
6. A database written by the pre-#26 binary opens under the new binary and vice
   versa, both directions checked with `PRAGMA integrity_check`. `run.sh`'s
   `cross` stage is the model.
7. `scripts/release-smoke.sh` passes against a release-style build.
8. Stripped linux/amd64 binary is under 14 MB. Measured 13,123,746 B; the
   headroom catches an accidental extra dependency.
9. `SaveCostRecord` stays under 250,000 ns/op at `-benchtime 2s`. Measured
   median 160,693; the bound is roughly the contended parallel figure and is
   there to catch an order-of-magnitude regression, not to police noise.
10. The licensing gate above is recorded in the pull request.

## What this spike did not test

- **Darwin at runtime.** Cross-compiled, never executed.
- **Windows.** Not a target today, not measured.
- **Real workloads.** Benchmarks are synthetic and single-host. There is no
  measurement of end-to-end proxy latency with either driver.
- **Long-running behaviour.** No soak test, no multi-GB database, no
  fragmentation or long-term WAL growth.
- **Corruption and crash recovery.** No process was killed mid-transaction.
- **Multi-process access at scale.** Two connections in one process was the
  limit; there is no test of several `oberwatch` processes on one file.
- **`modernc.org/sqlite` at any version other than v1.57.0.**
