# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
