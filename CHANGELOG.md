# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed
- Provider cards on the Settings page no longer disappear. A configured loopback Ollama keeps its card and reports `unreachable` when `GET /api/tags` does not answer, then returns to `operational` once Ollama is running. OpenAI and Anthropic cards stay visible with `status_unavailable` when their public status feeds cannot be read. No card is shown for Ollama when no base URL is configured
- The Settings page re-reads provider status every 15 seconds while it is open, so a provider that fails or recovers is reflected without a reload or a new login. A failed refresh keeps the last known cards and says so instead of blanking the grid

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
