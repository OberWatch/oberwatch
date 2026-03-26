# Contributing to Oberwatch

Thank you for your interest in contributing to Oberwatch.

## Development Setup

### Prerequisites

- Go 1.26+
- Node.js 22+ (for dashboard development)
- Make
- golangci-lint

### Clone and Build

```bash
git clone https://github.com/OberWatch/oberwatch.git
cd oberwatch
make build
```

### Running Tests

```bash
# Run all tests
make test

# Run tests with race detector
make test-race

# Run linter
make lint

# Format code
make fmt
```

## Code Style

### Go

- Follow standard Go conventions (`MixedCaps`, not `snake_case`)
- Every exported function, type, and constant gets a doc comment
- Keep functions short (under 50 lines)
- Accept interfaces, return structs
- Pass `context.Context` as the first parameter for I/O operations
- Use `log/slog` for structured logging (no third-party loggers)
- Use `fmt.Errorf("context: %w", err)` to wrap errors
- Never ignore errors

### Testing

- ALL tests must use table-driven style
- Use `t.Run()` for subtests
- Use `t.Helper()` in test helper functions
- Use `t.Parallel()` where safe
- Use `httptest.Server` for HTTP testing
- Never depend on network or external services in unit tests

### Dashboard (SvelteKit)

- TypeScript strict mode, no `any` types
- Tailwind CSS utility classes only
- One component per file
- Use Svelte 5 runes (`$state`, `$derived`)

## Pull Request Process

1. Fork the repository and create a feature branch
2. Write tests for any new functionality
3. Ensure all tests pass (`make test`) and linting is clean (`make lint`)
4. PR titles follow Conventional Commits format (see below)
5. One logical change per PR
6. PRs are squash-merged to main

## Commit Message Format

We use [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>: <description>

[optional body]
```

Types:
- `feat:` — New feature
- `fix:` — Bug fix
- `docs:` — Documentation only
- `test:` — Adding or updating tests
- `ci:` — CI/CD changes
- `refactor:` — Code change that neither fixes a bug nor adds a feature
- `chore:` — Maintenance tasks

Examples:

```
feat: add budget enforcement for per-agent spending caps
fix: correct token counting for streaming responses
docs: add quick start guide to README
test: add table-driven tests for cost calculation
```

## Dependencies

Do not add new dependencies without explicit approval. Approved dependencies:

- `github.com/BurntSushi/toml` — TOML config parsing
- `github.com/mattn/go-sqlite3` — SQLite driver
- `github.com/spf13/cobra` — CLI framework

No HTTP frameworks, ORMs, or assertion libraries.

## License

By contributing, you agree that your contributions will be licensed under the AGPL-3.0 License.
