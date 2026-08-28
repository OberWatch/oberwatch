# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.4] - 2026-08-28

### Added
- Delete an agent: `DELETE /agents/{name}` removes the agent record, its cost records, its alerts and its legacy budget snapshot in one transaction
- Task budgets are kept when their agent is deleted; only the `last_agent` pointer is cleared, so lifetime task totals and caps still apply
- The delete response reports how many rows of each kind were removed and that the agent is recreated on its next request
- An agent declared in the config file is rejected with `409 agent_protected`, because the next start would seed it again
- Delete action on the Agents page, behind a dialog that requires the agent name to be typed and states what is removed, what is kept, and that the next proxied request recreates the agent with the default budget
- `agent_deleted` SSE event, so an open Overview reloads when an agent is removed elsewhere

### Changed
- SQLite now uses the pure Go driver `modernc.org/sqlite` instead of `github.com/mattn/go-sqlite3`. Release binaries and the Docker image are built with `CGO_ENABLED=0` and are statically linked, so no C toolchain is needed to build them and no `libsqlite3` is needed to run them. Linux amd64 and arm64 remain the only published targets
- The database file is unchanged. Upgrading to this release, and downgrading from it, needs no data step: a database written by either version opens under the other at every schema version, with `PRAGMA integrity_check` reporting `ok`
- The bundled SQLite goes from 3.45.1 to 3.53.3, because the driver carries its own copy. No schema or query in Oberwatch depends on a version-specific feature
- Linux release binaries grow by about 2.2 MB: the stripped amd64 build goes from 10,923,760 to 13,164,706 bytes (+20.5%). Release tarballs and Docker layers grow by the same amount
- SQLite error text changes, which is visible in logs and in API error messages. A duplicate key now reads `constraint failed: UNIQUE constraint failed: settings.key (1555)` instead of `UNIQUE constraint failed: settings.key`, a missing table reads `SQL logic error: no such table: t`, and a lock timeout reads `database is locked (5) (SQLITE_BUSY)`. Anything that greps logs or parses these strings needs the new wording
- `alerts.webhook_url` must be an absolute `http` or `https` URL and `alerts.slack_webhook_url` must be an `https://hooks.slack.com/services/...` URL; `oberwatch validate` and startup reject anything else

### Fixed
- Webhook URLs, embedded credentials and tokens are redacted from alert logs and error messages, and a failed response body is truncated before it is logged
- Alert delivery runs off the request path through a bounded queue, so a slow or unreachable webhook no longer adds latency to proxied requests
- Each webhook attempt is cancelled after at most ten seconds; transport errors, 408, 429 and 5xx responses are retried up to three times with exponential backoff, other 4xx responses are not retried
- Each destination is delivered independently, so a failing generic webhook no longer holds up or fails a Slack alert
- A Slack alert with no message text no longer builds an empty block that Slack rejects with a permanent 400
- When the alert queue is full new alerts are dropped and logged at most once every thirty seconds with a drop count
- A provider card on the Settings page no longer disappears when its check fails: a configured loopback Ollama reports `unreachable` and returns to `operational` once it answers again, and OpenAI and Anthropic stay visible as `status_unavailable` when their public status feeds cannot be read
- No Ollama card is shown when no loopback base URL is configured
- The Settings page re-reads provider status every 15 seconds while it is open, so a failure or a recovery shows up without a reload, and a failed re-read keeps the last known cards instead of blanking the grid
- A provider card that has not been checked yet is shown as pending rather than coloured as a failure

## [0.1.3] - 2026-08-27

### Added
- Per-task budgets: `gate.task_budget_usd` and per-agent `task_budget_usd` cap the lifetime spend of a task identified by `X-Oberwatch-Task`
- Task cap checks include in-flight estimates and reject with a structured `task_budget_exceeded` 429 before the request reaches the provider
- Task spend totals persisted in SQLite (schema v4) and restored on restart
- `GET /tasks`, `GET /tasks/{task_id}`, and `POST /tasks/{task_id}/reset` management endpoints
- `task` filter on `GET /costs` and `GET /costs/export`
- `oberwatch init` writes `./oberwatch.toml` by default, supports `--output` and `--force`, and refuses to overwrite an existing file, directory or symlink without `--force`
- `oberwatch validate` prints `config <path> is valid` on success, names the file and line for malformed TOML, lists unknown keys and each failing semantic check, and sends onboarding warnings to stderr
- `scripts/release-smoke.sh` and `make smoke` run the init/validate, runaway kill/enable and task budget contracts against one release-style binary and check that it reports the newest CHANGELOG version
- `scripts/dev/test-runaway.sh` and `scripts/dev/test-task-budget.sh` end-to-end checks against a mock upstream, and `make test-cli` for `scripts/test-cli-config.sh`

### Changed
- `GET /tasks` reports the task cap that the next request would be held to instead of the last cap that applied
- Task totals are flushed only for the task that settled, and a failed flush keeps every pending total so a restart does not under-count spend
- `RenameAgent` carries the per-agent task cap and recorded agent across the rename

### Fixed
- CLI failures were printed twice, once by the command parser and once by `main`
- `config.Load`, `validate` and the root `--config` flag resolve the config path through one loader, so a missing or unknown-key file fails the same way at runtime as under `validate`
- A missing config found through the search order reported `--config flag` as its source
- The starter, example and installer configs no longer describe `admin_token` as a required bearer token; management auth is session-based
- Provider status reporting reflects the real upstream state
- `make tools` and `make lint` failed with `Permission denied` because `scripts/dev/install-tools.sh` had no executable bit
- README listed a project doc that is not in the repository

## [0.1.2] - 2026-08-26

### Added
- `last_model` and ordered `models_used` fields on `GET /agents`
- Sortable monospace Model column on the Agents page, with a keyboard-reachable list of every model an agent has used
- Custom start and end dates on the Costs page, validated for required values and start before end
- `agent_hour` cost grouping that keeps both the agent and the hour bucket, so the cost-over-time chart is genuinely stacked per agent
- Shared skeleton, KPI, table and chart loading components used by Overview, Agents, Costs and Settings
- Shared error state with a Retry action that keeps the active range and filters
- Cost range and time bucket reference in `docs/costs-date-ranges.md`

### Changed
- Costs `Today` now covers the local calendar day instead of a rolling 24 hours, and one selected range drives the totals, charts, table and CSV export
- Loading placeholders stop pulsing when the viewer prefers reduced motion
- A loaded zero or empty list is rendered as an empty state rather than as loading placeholders

### Fixed
- Ungrouped cost queries order by normalized timestamp then row ID, so latest-model selection is deterministic when timestamps tie
- The Linux installer systemd unit sets `OBERWATCH_TRACE__SQLITE_PATH`, so the service starts, and the install summary prints the paths the service actually uses
- Docker Hub image path in the README deployment channel table

## [0.1.1] - 2026-08-25

### Added
- Model downgrade response headers (`X-Oberwatch-Downgraded` and `X-Oberwatch-Original-Model`)
- Built-in default downgrade chains for OpenAI, Anthropic, and Google/Gemini models
- Global budget cap enforcement across all agents
- In-flight SSE token counting with delta-content fallback
- Agent identification through configured API key mappings
- Complete cost query filtering and CSV export contract

### Fixed
- Portable, upgrade-safe Linux installer behavior
- Docker image startup and health-check verification

## [0.1.0] - 2026-03-30

### Added
- Reverse proxy for OpenAI, Anthropic, Google, and Ollama APIs
- Streaming (SSE) passthrough with accurate token counting
- Agent auto-registration on first proxied request via X-Oberwatch-Agent header
- Per-agent budget enforcement with configurable actions: reject, downgrade, alert, kill
- Model auto-downgrade chains when budget threshold is reached
- Global budget cap with configurable period (hourly, daily, weekly, monthly)
- Runaway detection with configurable request rate window and auto-kill
- Emergency stop with resume — pauses all agent proxy traffic without affecting dashboard or API
- Agent management via dashboard: rename, edit budgets, kill, enable, reset
- Budget persistence in SQLite with periodic in-memory to disk flush
- Webhook and Slack alert integrations with deduplication
- Dashboard: Overview, Agents, Costs, and Settings pages
- Real-time dashboard updates via Server-Sent Events
- First-run onboarding wizard with admin account creation
- Session-based authentication with 24h expiry and password change
- Built-in model pricing tables for OpenAI, Anthropic, and Google models
- One-line install script for Linux with systemd service setup
- Docker support with named volume persistence
- Docker Compose template with commented enterprise service placeholders
- GHCR image publishing: beta (from main), staging (from staging branch)
- Docker Hub image publishing on tagged releases
- CI/CD: lint, test, 80% coverage gate, dashboard build, automated releases via GoReleaser
- Docker volume mount detection with startup warning when data is not persisted
