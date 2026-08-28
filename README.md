<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="dashboard/svelte/static/logo-white.svg" />
    <source media="(prefers-color-scheme: light)" srcset="dashboard/svelte/static/logo.svg" />
    <img src="dashboard/svelte/static/logo.svg" alt="Oberwatch logo" width="120" />
  </picture>
</p>

<h1 align="center">Oberwatch</h1>

<p align="center">
  Open-source proxy, spend governor, and operational dashboard for AI agents.
  <br />
  Route agent traffic through one endpoint, track real model costs, enforce budgets, and keep a live control plane in front of your providers.
</p>

<p align="center">
  <a href="https://github.com/OberWatch/oberwatch/actions/workflows/ci.yml"><img src="https://github.com/OberWatch/oberwatch/actions/workflows/ci.yml/badge.svg" alt="CI" /></a>
  <a href="https://goreportcard.com/report/github.com/OberWatch/oberwatch"><img src="https://goreportcard.com/badge/github.com/OberWatch/oberwatch" alt="Go Report Card" /></a>
  <a href="https://www.gnu.org/licenses/agpl-3.0"><img src="https://img.shields.io/badge/License-AGPL--3.0-blue.svg" alt="License: AGPL-3.0" /></a>
</p>

---

## Why Oberwatch

AI agents are expensive, fast-moving, and easy to lose control of.
Oberwatch puts a control layer in front of them:

- One proxy endpoint for agent traffic
- Per-agent spend tracking with real pricing tables
- Budget thresholds, alerts, downgrade paths, reject/kill enforcement
- Live dashboard for costs, agents, settings, and emergency controls
- SQLite-backed persistence for agents, costs, alerts, sessions, and runtime state
- First-run onboarding with single-user session auth

## What Ships Today

| Area | What you get |
| --- | --- |
| Proxy | OpenAI-compatible and Anthropic-style traffic routed through a single Oberwatch endpoint |
| Budgets | Per-agent and default budgets with `alert`, `reject`, `downgrade`, and `kill` actions |
| Alerts | Threshold alerts at configurable percentages, live dashboard toasts, webhook and Slack dispatch |
| Dashboard | Overview, Agents, Costs, and Settings pages with live SSE updates |
| Auth | First-run setup flow, login/logout, cookie sessions, password change |
| Persistence | SQLite storage for costs, alerts, agents, settings, and emergency stop state |
| Safety | Emergency stop that pauses agent traffic while keeping the management UI/API online |
| Pricing | Built-in pricing defaults plus config overrides, including streaming usage extraction |

## How It Works

```text
Agent SDK / app
      |
      v
  Oberwatch proxy
      |
      +--> identifies agent
      +--> checks budget policy
      +--> forwards to provider
      +--> extracts token usage
      +--> calculates cost
      +--> stores spend + alerts + traces/state
      |
      v
 Dashboard + API + SQLite
```

## Quick Start

### One-line install (Linux)

```bash
curl -fsSL https://raw.githubusercontent.com/OberWatch/oberwatch/main/scripts/install.sh | sh
```

Then open `http://localhost:8080` to complete setup.
### Docker

```bash
docker run -d --name oberwatch -p 8080:8080 -v oberwatch-data:/data ghcr.io/oberwatch/oberwatch:beta
```

The container starts without a config file and stores `./oberwatch.db` in the named `/data` volume, so data survives container replacement. Open `http://localhost:8080` to complete setup. No admin token is required; first-run setup creates the admin account and starts a cookie-backed session.


### Docker Compose (for Enterprise Edition)

```bash
wget https://raw.githubusercontent.com/OberWatch/oberwatch/main/docker-compose.yml
# Edit to uncomment enterprise services and add license key
docker compose up -d
```

### Build from source

```bash
git clone https://github.com/OberWatch/oberwatch.git
cd oberwatch
make build
./bin/oberwatch serve
```

### Local development

```bash
git clone https://github.com/OberWatch/oberwatch.git
cd oberwatch
make dev
```

`make dev` is the default local workflow. It runs:
- the Go backend with `air` for automatic rebuild/restart
- the Svelte dashboard dev server with hot reload
- a proxy from `/_oberwatch/*` to `http://localhost:8080`


## First-Run Experience

Oberwatch is designed to boot cleanly on a fresh machine or hosted platform.

1. Start the binary or container.
2. Open the dashboard.
3. Create the single admin account in the onboarding flow.
4. Sign in.
5. Point agents at the proxy URL shown by Oberwatch.

Management auth is session-based. After setup, the dashboard and management API use secure cookies instead of a shared dashboard token; no admin token is required.

## Pointing Agents At Oberwatch

Use Oberwatch as the base URL your agent runtime talks to, and send an agent identity header.

Example OpenAI-compatible request:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -H "X-Oberwatch-Agent: research-agent" \
  -d '{
    "model": "gpt-4.1-mini",
    "messages": [{"role": "user", "content": "Summarize the latest sprint notes."}]
  }'
```

That agent name becomes the key for:
- budget enforcement
- cost tracking
- alerts
- dashboard visibility
- runtime controls such as reset, enable, rename, and kill

## Budget Enforcement

Oberwatch can enforce both shared defaults and per-agent overrides.

Supported actions when spend crosses policy limits:
- `alert`
- `reject`
- `downgrade`
- `kill`

Current behavior includes:
- configurable threshold alerts, commonly `50`, `80`, `100`
- automatic live dashboard notifications when thresholds are crossed
- persistent kill state for manual and runaway kills
- automatic recovery after period reset for budget-exceeded kills
- emergency stop across all agent traffic without taking the control plane offline

### Per-task budgets

Set `gate.task_budget_usd` to cap the lifetime spend of one task. A task is
identified by the `X-Oberwatch-Task` request header, which is stripped before
the request reaches the provider. Requests without the header, or with a blank
value, are not task-budgeted and never share a bucket. A `[[gate.agents]]`
entry can set its own `task_budget_usd`; a value above zero is preferred over
the gate value, zero inherits it. `task_budget_usd = 0` at the gate level
disables task budgets.

Task caps and agent budgets are tracked separately and enforced separately. A
request must fit both: its cost is added to the agent period budget and to the
task total. Resetting an agent, or an agent period rollover, does not touch task
spend, and resetting a task does not touch agent spend. The same task ID used by
several agents shares one total, capped by the task limit of whichever agent
sends the request.

Before a request is sent upstream, Oberwatch adds the settled spend of the task,
the estimated cost of requests still in flight, and the estimate for the new
request. If that projection exceeds the cap the request is rejected with HTTP
429 and this body:

```json
{"error":{"code":"task_budget_exceeded","message":"...","agent":"research-agent","task_id":"job-42",
  "task_budget_limit_usd":5,"task_budget_spent_usd":4.9,"task_budget_reserved_usd":0.05,"task_budget_projected_usd":5.1}}
```

Settled task totals are persisted in SQLite and restored on restart. In-flight
estimates are not persisted because an interrupted request never completes.

Management endpoints:
- `GET /_oberwatch/api/v1/tasks` lists task budgets
- `GET /_oberwatch/api/v1/tasks/{task_id}` returns one task
- `POST /_oberwatch/api/v1/tasks/{task_id}/reset` clears the settled spend of a task
- `GET /_oberwatch/api/v1/costs?task={task_id}` and `/costs/export?task={task_id}` filter cost rows on the exact task ID

## Dashboard

The embedded dashboard is part of the main binary.

Current pages:
- `Overview` for spend, active agents, alerts, uptime, and emergency stop
- `Agents` for runtime status, model history, budget usage, edit panel, rename, kill/enable/reset
- `Costs` for totals, breakdowns, charts, and CSV export
- `Settings` for system health, provider status, pricing, and password change

`GET /_oberwatch/api/v1/agents` returns `last_model` and `models_used` for each agent. `models_used` is distinct and ordered from most recently used to least recently used. `last_model` is its first value. If records share a timestamp, descending cost-record ID breaks the tie. Agents without cost records return `last_model: ""` and `models_used: []`.

The UI uses server-sent events for live updates, including:
- budget threshold toasts
- emergency stop changes
- cost refresh triggers
- live alert visibility

## Pricing and Cost Calculation

Oberwatch tracks real costs instead of raw token counts alone.

It currently supports:
- built-in pricing defaults for OpenAI, Anthropic, and Google models
- config-based pricing overrides
- extraction of usage from standard response bodies
- streaming usage accumulation for providers that emit final usage data
- fallback estimation when providers omit usage entirely

Very small non-zero amounts are displayed with higher precision so sub-cent spend does not collapse to `$0.00`.

## Configuration

Start with the example config:

- [oberwatch.example.toml](./oberwatch.example.toml)

You can run fully config-free, but a config file becomes useful when you want to define:
- explicit agent budgets
- downgrade chains
- alert thresholds
- upstream base URLs
- trace retention and SQLite paths
- webhook or Slack alert destinations

Environment variables override TOML using double-underscore nesting.

Examples:

```bash
export OBERWATCH_SERVER__PORT=8080
export OBERWATCH_GATE__ENABLED=true
export OBERWATCH_TRACE__STORAGE=sqlite
```

## Production Deployment

If you expose Oberwatch on the public internet, put it behind a reverse proxy such as Caddy or Nginx and terminate TLS there.

Warning: Never expose Oberwatch without TLS in production. API keys pass through the proxy in request headers.

See [production.md](./docs/production.md) for the production deployment guide placeholder.

## Deployment and Release Channels

Oberwatch publishes images across two registries with different intent.

| Channel | Registry | Purpose |
| --- | --- | --- |
| `latest`, `0.1`, `0.1.4` | Docker Hub + GHCR | Stable tagged releases |
| `beta` | GHCR | Preview build from `main` |
| `staging` | GHCR | Integration/staging build |
| `sha-<commit>` | GHCR | Immutable branch build for debugging and verification |

Recommended usage:
- Production: `kaissb/oberwatch:0.1.4` or `kaissb/oberwatch:latest`
- Preview testing: `ghcr.io/oberwatch/oberwatch:beta`
- Staging environments: `ghcr.io/oberwatch/oberwatch:staging`

## CLI

Oberwatch ships as a Cobra-based CLI.

Primary commands:
- `oberwatch serve`
- `oberwatch gate`
- `oberwatch trace`
- `oberwatch test run`
- `oberwatch validate`
- `oberwatch init`
- `oberwatch version`

## Repo Guide

Core paths:
- [cmd/oberwatch](./cmd/oberwatch) - CLI entrypoint
- [internal/api](./internal/api) - management API and auth
- [internal/proxy](./internal/proxy) - provider proxy path
- [internal/budget](./internal/budget) - budget tracking and enforcement
- [internal/pricing](./internal/pricing) - pricing engine and usage extraction
- [internal/storage](./internal/storage) - SQLite persistence
- [dashboard/svelte](./dashboard/svelte) - Svelte dashboard source

Project docs:
- [CONTRIBUTING.md](./CONTRIBUTING.md)
- [BRANCHING.md](./BRANCHING.md)
- [SECURITY.md](./SECURITY.md)
- [CHANGELOG.md](./CHANGELOG.md)
- [AI_CODING.md](./AI_CODING.md)

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md) for local setup, checks, commit conventions, and workflow details.

The short version:

```bash
make dev
```

Then before sending work upstream, run the same checks CI expects.

## License

Oberwatch is licensed under the [GNU Affero General Public License v3.0](./LICENSE).

Copyright (C) 2026 Bouali Consulting Inc.
