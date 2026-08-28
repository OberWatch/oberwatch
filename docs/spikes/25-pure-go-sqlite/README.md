# Spike: pure Go SQLite driver (issue #25)

Decision spike, not a migration. The question is whether Oberwatch can drop the
cgo requirement by replacing `github.com/mattn/go-sqlite3` with a pure Go SQLite
driver, without changing the on-disk database or the behaviour of
`internal/storage`.

The answer is yes, on `modernc.org/sqlite v1.57.0`. The reasoning, the
tradeoffs and the contract for issue #26 are in [DECISION.md](DECISION.md).
This file explains how to regenerate the evidence.

## Layout

```
run.sh                       harness; regenerates everything in logs/
harness/spike_driver_test.go behaviour tests run against each driver
harness/driver_mattn.go      driver adapter: github.com/mattn/go-sqlite3
harness/driver_modernc.go    driver adapter: modernc.org/sqlite
harness/driver_ncruces.go    driver adapter: github.com/ncruces/go-sqlite3
logs/                        recorded output; see the stage table below
DECISION.md                  evidence, decision, contract for #26
```

All four files in `harness/` carry `//go:build ignore` so they never take part
in a normal repo build. `run.sh` strips that line when it copies them into a
scratch tree.

## How it works

Nothing in the production tree changes. For each driver `run.sh`:

1. rsyncs the working tree to `$SPIKE_WORK/<driver>` (default
   `~/.cache/oberwatch-spike-25`),
2. rewrites `internal/storage/sqlite.go` to take the driver name and the
   constraint-error check from a separate file (four hunks; see
   `patch_storage`),
3. drops in `harness/driver_<driver>.go` as `internal/storage/driver_spike.go`
   and `harness/spike_driver_test.go` as `internal/storage/spike_driver_test.go`,
4. pins the driver version and runs `go mod tidy`.

The mattn baseline gets the same treatment, so the three trees differ only in
the adapter file and the pinned driver.

## Running it

```sh
cd docs/spikes/25-pure-go-sqlite
./run.sh all                      # every stage, ~20 min on 8 cores
./run.sh prepare                  # just build and vet the three scratch trees
./run.sh tests cross              # behaviour and on-disk compatibility only
GO=/path/to/go ./run.sh bench     # pick a specific toolchain
```

## Stages

| Stage      | Log                             | What it answers                                                   |
| ---------- | ------------------------------- | ----------------------------------------------------------------- |
| `env`      | `00-environment.log`            | toolchain, host, pinned driver versions                            |
| `baseline` | `01-…-builds.log` `02-…-tests.log` | what mattn costs today and which targets it cannot build        |
| `deps`     | `03-deps-licenses-vulns.log`    | dependency count, licenses, `govulncheck`                          |
| `matrix`   | `04-pure-go-build-matrix.log`   | cross-compiles, binary size, build time, static linkage            |
| `tests`    | `10-<driver>-tests.log`         | v1–v4 migration, pragmas, busy timeout, concurrency, shutdown      |
| `cross`    | `11-cross-driver-fixtures.log`  | every driver opens every other driver's database files             |
| `bench`    | `12-benchmarks.log`             | insert, query, upsert and open/migrate throughput                  |
| `race`     | `13-race.log`                   | race detector over the harness and the real suite                  |

`baseline` and `matrix` call `go clean -cache` to get honest cold-build numbers,
so they slow down anything else compiling on the same machine.

## Caveats

- Every number was measured on one host; see `00-environment.log`. Build times
  in particular are machine-specific.
- Darwin builds are cross-compiled and were not executed. They prove the
  toolchain produces Mach-O binaries, not that the binaries run.
- `-race` needs cgo, so stage `race` builds the pure Go drivers with
  `CGO_ENABLED=1`. That applies to the test binary only; the shipped artifact is
  the `CGO_ENABLED=0` build from stage `matrix`.
