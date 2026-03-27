# Oberwatch AI Coding Rules

## ALWAYS INCLUDE THIS FILE IN EVERY CODING SESSION

This file defines mandatory coding standards for any AI assistant working on the Oberwatch codebase. These rules are non-negotiable.

---

## Project Context

Oberwatch is an open-source proxy and observability platform for AI agents. It is written in Go with a SvelteKit dashboard. Read ARCHITECTURE.md for system design details.

Repository structure:
- `cmd/oberwatch/` — CLI entry point
- `internal/` — Private packages (proxy, budget, trace, eval, config, alert, provider, pricing, dashboard)
- `pkg/` — Public packages (types, client)
- `sdk/` — Python and JS client libraries
- `dashboard/svelte/` — SvelteKit UI source
- `testdata/` — Test fixtures
- `docs/` — Public documentation

---

## Go Coding Rules

### Error Handling
- NEVER ignore errors. Every error must be handled explicitly.
- Use `fmt.Errorf("context: %w", err)` to wrap errors with context.
- Do NOT use `log.Fatal` or `os.Exit` outside of `main.go`.
- Return errors up the call stack. Let the caller decide what to do.
- Use custom error types (implementing `error` interface) for errors that callers need to distinguish.

```go
// GOOD
result, err := doSomething()
if err != nil {
    return fmt.Errorf("processing invoice for agent %s: %w", agentName, err)
}

// BAD
result, _ := doSomething()
```

### Naming
- Follow Go conventions: `MixedCaps`, not `snake_case`.
- Interfaces: use `-er` suffix when describing behavior (`Reader`, `Checker`, `Enforcer`).
- Avoid stuttering: `budget.Budget` is fine, `budget.BudgetBudget` is not.
- Acronyms are all caps: `ID`, `HTTP`, `URL`, `API`, `SSE`, `LLM`, `USD`.
- Test functions: `TestFunctionName_Scenario` (e.g., `TestCheckBudget_ExceedsLimit`).

### Documentation
- Every exported function, type, and constant gets a doc comment.
- Doc comments start with the name of the thing being documented.
- Package-level doc comments go in a `doc.go` file.

```go
// CheckBudget evaluates whether the given cost would exceed the agent's budget.
// It returns the enforcement action to take, or an empty string if the request is allowed.
func CheckBudget(budget Budget, costUSD float64) (BudgetAction, error) {
```

### Functions
- Keep functions short. If a function exceeds 50 lines, consider splitting it.
- Functions should do one thing.
- Accept interfaces, return structs.
- Pass `context.Context` as the first parameter for anything that does I/O.
- No global mutable state. Pass dependencies explicitly.

### Concurrency
- Use `sync.RWMutex` for in-memory state that is read often, written rarely.
- Use channels for communication between goroutines.
- Always use `context.Context` for cancellation and timeouts.
- Never launch goroutines without a way to stop them (context, done channel, or WaitGroup).

### Logging
- Use `log/slog` (Go standard library structured logging). No third-party loggers.
- Log levels: `Debug` for development, `Info` for normal operations, `Warn` for recoverable issues, `Error` for failures.
- Always include relevant context in log messages.

```go
slog.Info("request proxied",
    "agent", agentName,
    "model", model,
    "cost_usd", cost,
    "duration_ms", duration.Milliseconds(),
)
```

### Testing (CRITICAL)
- ALL tests MUST use table-driven style. No exceptions.
- Use `t.Run()` for subtests.
- Use `t.Helper()` in test helper functions.
- Use `t.TempDir()` for temporary files. Never hardcode paths.
- Use `t.Parallel()` where safe.
- Never depend on network, file system state, or external services in unit tests.
- Use `httptest.Server` for HTTP testing.
- Name test cases descriptively: they should read like documentation.

```go
func TestCalculateCost(t *testing.T) {
    t.Parallel()
    tests := []struct {
        name         string
        model        string
        inputTokens  int
        outputTokens int
        wantCostUSD  float64
    }{
        {
            name:         "gpt-4o standard pricing",
            model:        "gpt-4o",
            inputTokens:  1000,
            outputTokens: 500,
            wantCostUSD:  0.0075,
        },
        // ... more cases
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            t.Parallel()
            got := CalculateCost(tt.model, tt.inputTokens, tt.outputTokens)
            if got != tt.wantCostUSD {
                t.Errorf("CalculateCost() = %v, want %v", got, tt.wantCostUSD)
            }
        })
    }
}
```

### Dependencies
- Minimize external dependencies. Prefer the standard library.
- Approved dependencies:
  - `github.com/BurntSushi/toml` — TOML config parsing
  - `github.com/mattn/go-sqlite3` — SQLite driver
  - `github.com/spf13/cobra` — CLI framework
  - No HTTP framework (use `net/http` standard library)
  - No ORM (write SQL directly)
  - No assertion libraries (use standard `testing` package)

### Code Organization
- `internal/` packages are not importable by external code. Use for implementation details.
- `pkg/` packages are the public API. Be very intentional about what goes here.
- Each package should have a clear, single responsibility.
- No circular dependencies between packages.

---

## SvelteKit / Dashboard Rules

### TypeScript
- Strict mode enabled. No `any` types.
- All component props must be typed.
- All API responses must have type definitions matching the Go types in DATA_MODELS.md.

### Components
- One component per file.
- Use `$state` and `$derived` runes (Svelte 5), not stores.
- Keep components small — if a component exceeds 150 lines, split it.

### Styling
- Tailwind CSS utility classes only. No custom CSS unless absolutely necessary.
- Dark mode first, light mode as override.
- Consistent spacing: use Tailwind's spacing scale.

### Data Fetching
- All API calls go through a single `api.ts` utility module.
- Handle loading, error, and empty states for every data fetch.
- Use SSE (`EventSource`) for real-time data, not polling.

---

## Git Rules

- Commit messages follow Conventional Commits: `feat:`, `fix:`, `docs:`, `test:`, `ci:`, `refactor:`, `chore:`
- One logical change per commit.
- PR titles follow the same format.
- Squash merge to main.

## Required End-of-Task Checks (MANDATORY)

At the end of every coding task, run these commands from the `oberwatch/` repo root in this order:

1. Format:
  - `PATH=/usr/local/go/bin:$PATH /usr/local/go/bin/gofmt -w .`
2. Lint (must match CI linter/version behavior):
  - `PATH=/usr/local/go/bin:$PATH GOCACHE=/tmp/go-build GOLANGCI_LINT_CACHE=/tmp/golangci-cache ./.tools/bin/golangci-lint run`
3. Tests (same as CI):
  - `PATH=/usr/local/go/bin:$PATH GOCACHE=/tmp/go-build /usr/local/go/bin/go test -race -coverprofile=coverage.out ./...`
4. Coverage gate (same as CI threshold):
  - `COVERAGE=$(PATH=/usr/local/go/bin:$PATH /usr/local/go/bin/go tool cover -func=coverage.out | grep total | awk '{print $3}' | sed 's/%//') && echo "Total coverage: ${COVERAGE}%" && if (( $(echo "$COVERAGE < 80" | bc -l) )); then echo "Coverage ${COVERAGE}% is below 80% threshold"; exit 1; fi`
5. Vet:
  - `PATH=/usr/local/go/bin:$PATH GOCACHE=/tmp/go-build /usr/local/go/bin/go vet ./...`
6. Dashboard deps:
  - `cd dashboard/svelte && npm ci`
7. Dashboard build:
  - `cd dashboard/svelte && npm run build`
8. Dashboard check:
  - `cd dashboard/svelte && npm run check`
9. TypeScript linting:
  - `cd dashboard/svelte && npm run lint:ts`

If all checks pass, automatically generate a Conventional Commit message proposal summarizing the change.
Do not create or amend a commit unless explicitly requested by the user.

## Local Development Workflow (MANDATORY)

- Use `make dev` from the `oberwatch/` repo root for active implementation and manual local testing.
- `make dev` runs the Go backend with `air` and the Svelte dev server concurrently.
- The Svelte dev server must proxy `/_oberwatch/*` requests to `http://localhost:8080`.

---

## What NOT to Do

- Do NOT use `panic()` except in truly unrecoverable situations (and even then, prefer returning an error).
- Do NOT use `init()` functions. Initialize explicitly in `main()` or constructors.
- Do NOT use global variables for mutable state.
- Do NOT use `interface{}` or `any` — use concrete types or specific interfaces.
- Do NOT add dependencies without explicit approval in the PR description.
- Do NOT write tests that sleep (`time.Sleep`). Use channels, contexts, or test clocks.
- Do NOT hardcode port numbers, file paths, or URLs. Use config.
- Do NOT log sensitive data (API keys, prompt content, user data).
- DO NOT empty file contents without explicit user consent in the current request.
