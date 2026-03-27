# Oberwatch

Open-source proxy and observability platform for AI agents. Budget enforcement, decision tracing, and behavioral testing in a single binary.

<!-- Badges -->
[![CI](https://github.com/OberWatch/oberwatch/actions/workflows/ci.yml/badge.svg)](https://github.com/OberWatch/oberwatch/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/OberWatch/oberwatch)](https://goreportcard.com/report/github.com/OberWatch/oberwatch)
[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL--3.0-blue.svg)](https://www.gnu.org/licenses/agpl-3.0)

## Quick Start

### Docker

```bash
docker run -p 8080:8080 oberwatch/oberwatch:latest serve
```

With a config file:

```bash
docker run -p 8080:8080 \
  -v ./oberwatch.toml:/etc/oberwatch/oberwatch.toml:ro \
  -v oberwatch-data:/data \
  oberwatch/oberwatch:latest
```

### Docker Compose

```bash
curl -O https://raw.githubusercontent.com/OberWatch/oberwatch/main/docker-compose.yml
docker compose up
```

### Pre-built Binary

Download the latest release for your platform from [GitHub Releases](https://github.com/OberWatch/oberwatch/releases).

```bash
# Linux (amd64)
curl -L https://github.com/OberWatch/oberwatch/releases/latest/download/oberwatch-linux-amd64 -o oberwatch
chmod +x oberwatch
./oberwatch serve
```

### Build from Source

```bash
git clone https://github.com/OberWatch/oberwatch.git
cd oberwatch
make build
./bin/oberwatch serve
```

### Local Development

```bash
git clone https://github.com/OberWatch/oberwatch.git
cd oberwatch
make dev
```

`make dev` runs the Go backend with `air` and the Svelte dev server concurrently. The dashboard dev server proxies `/_oberwatch/*` to `http://localhost:8080`.

## Documentation

Key repo docs:
- [CONTRIBUTING.md](./CONTRIBUTING.md)
- [BRANCHING.md](./BRANCHING.md)
- [CLAUDE.md](./CLAUDE.md)

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md) for development setup, coding standards, and PR conventions.

## License

Oberwatch is licensed under the [GNU Affero General Public License v3.0](LICENSE).

Copyright (C) 2026 Bouali Consulting Inc.
