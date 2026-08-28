# License gate: modernc.org SQLite driver

**Status: open. This gate must be closed by a human before the issue #26 branch
merges.** The code is finished and the tests pass; the licensing question is the
remaining blocker.

Issue #25 approved replacing `github.com/mattn/go-sqlite3` (MIT throughout) with
`modernc.org/sqlite v1.57.0`. That decision recorded one condition on the
migration: two of the new modules were not classified by tooling, so a person
has to look at them and say yes or no in writing.

## What the tooling reported

From `docs/spikes/25-pure-go-sqlite/logs/03-deps-licenses-vulns.log`,
`go-licenses report ./internal/storage` on the modernc tree:

| Module | Reported license | Reported source |
| --- | --- | --- |
| `modernc.org/sqlite` v1.57.0 | BSD-3-Clause | `LICENSE` |
| `modernc.org/memory` v1.11.0 | BSD-3-Clause | `LICENSE-GO` |
| `modernc.org/libc` v1.74.4 | MIT | `LICENSE-3RD-PARTY.md` |
| `modernc.org/mathutil` v1.7.1 | **Unknown** | **Unknown** |

Two results are not usable as a license review:

1. **`modernc.org/mathutil` is Unknown.** The tool could not classify the file
   at all. "Unclassified" is not the same as "permissive".
2. **`modernc.org/libc` resolved to the wrong file.** `LICENSE-3RD-PARTY.md`
   lists the terms of code the module vendored from elsewhere. It is not the
   module's own license. The module also ships a plain `LICENSE`, which the tool
   did not report.

## What is actually in the module cache

Recorded so the reviewer knows which files to open, not as a determination. Both
paths are relative to the module root as published on the Go module proxy.

- `modernc.org/mathutil@v1.7.1/LICENSE` — 27 lines, one file, no SPDX
  identifier. Opens `Copyright (c) 2014 The mathutil Authors. All rights
  reserved.` and carries the three conditions of a BSD-3-Clause text, including
  the no-endorsement clause.
- `modernc.org/libc@v1.74.4/LICENSE` — 27 lines, the same shape, opening
  `Copyright (c) 2017 The Libc Authors. All rights reserved.`
- `modernc.org/libc@v1.74.4/LICENSE-3RD-PARTY.md` — 305 lines. States that the
  module itself is BSD-3 and lists four vendored components with their own
  terms: Go, musl libc, go-netdb and NixOS/nixpkgs.

The likely reason the tool failed on both is that neither file carries an SPDX
identifier or the exact wording go-licenses matches on. That is an explanation,
not a clearance.

## What a human has to confirm

The reviewer needs to record, in the issue #26 pull request, an explicit
statement covering all four points:

1. `modernc.org/mathutil@v1.7.1` — the license in its `LICENSE` file, named,
   and whether it is permissive and compatible with distributing Oberwatch under
   AGPL-3.0.
2. `modernc.org/libc@v1.74.4` — the license in its own `LICENSE` file, named,
   with the same compatibility judgement.
3. `modernc.org/libc@v1.74.4` third-party components — that the terms of the
   four vendored components in `LICENSE-3RD-PARTY.md` are acceptable, since they
   ship inside the binary.
4. Whether any attribution or notice text has to be added to this repository or
   to the release artifacts as a condition of those licenses. A BSD-3-Clause
   dependency requires the copyright notice to be reproduced in binary
   distributions.

Confirmation belongs in the pull request, per acceptance criterion 10 in the
issue #25 decision. Point at the file and the version. "go-licenses says BSD" is
not enough, because for these two modules it does not.

## If the answer is no

If either module is not permissive, or is incompatible with AGPL-3.0
distribution, then issue #26 stops and the issue #25 decision is reopened. The
migration is a revert of one pull request with no data step, so there is nothing
to unwind on disk. The recorded alternative in the decision,
`github.com/ncruces/go-sqlite3`, has the same class of problem
(`github.com/ncruces/go-sqlite3-wasm/v3` also reported Unknown), so a rejection
here likely means staying on cgo rather than switching drivers again.

## Scope

This gate is about licensing only. The other merge condition from issue #25,
keeping `-race` in CI on `CGO_ENABLED=1`, is satisfied: the workflows still run
`go test -race` on the default cgo-enabled runner, and only the shipped
artifacts move to `CGO_ENABLED=0`.
