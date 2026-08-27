package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// StarterTOML is the commented starter config written by `oberwatch init`.
const StarterTOML = `# =============================================================================
# Oberwatch Configuration
# =============================================================================

# -----------------------------------------------------------------------------
# Server Settings
# -----------------------------------------------------------------------------
[server]
# Port the proxy listens on.
# Default: 8080
# Env: OBERWATCH_SERVER__PORT
port = 8080

# Bind address.
# Default: "0.0.0.0"
# Env: OBERWATCH_SERVER__HOST
host = "0.0.0.0"

# Legacy admin token. Management API and dashboard auth is session-based:
# the first visit to the dashboard creates the admin account. Leave empty.
# Env: OBERWATCH_SERVER__ADMIN_TOKEN
admin_token = ""

# Enable the embedded dashboard.
# Default: true
# Env: OBERWATCH_SERVER__DASHBOARD
dashboard = true

# Log level: debug, info, warn, error
# Default: "info"
# Env: OBERWATCH_SERVER__LOG_LEVEL
log_level = "info"

# Log format: json, text
# Default: "text"
# Env: OBERWATCH_SERVER__LOG_FORMAT
log_format = "text"

# TLS certificate and key files. If both are set, the server uses HTTPS.
# Env: OBERWATCH_SERVER__TLS_CERT / OBERWATCH_SERVER__TLS_KEY
tls_cert = ""
tls_key = ""

# -----------------------------------------------------------------------------
# Upstream Provider Configuration
# -----------------------------------------------------------------------------
[upstream]
# Default upstream provider when auto-detection is ambiguous.
# Options: "openai", "anthropic", "google", "ollama", "custom"
# Default: "openai"
default_provider = "openai"

# Request timeout for upstream calls.
# Default: "120s"
timeout = "120s"

[upstream.openai]
# Base URL for OpenAI API. Change for Azure OpenAI or compatible providers.
# Default: "https://api.openai.com"
base_url = "https://api.openai.com"

[upstream.anthropic]
# Base URL for Anthropic API.
# Default: "https://api.anthropic.com"
base_url = "https://api.anthropic.com"

[upstream.google]
# Base URL for Google Gemini OpenAI-compatible API.
# Default: "https://generativelanguage.googleapis.com/v1beta/openai"
base_url = "https://generativelanguage.googleapis.com/v1beta/openai"

[upstream.ollama]
# Base URL for Ollama.
# Default: "http://localhost:11434"
base_url = "http://localhost:11434"

[upstream.custom]
# Base URL for any OpenAI-compatible provider (Together, Groq, etc.)
base_url = ""

# -----------------------------------------------------------------------------
# Gate (Cost Governor) Settings
# -----------------------------------------------------------------------------
[gate]
# Enable the gate (cost tracking and budget enforcement).
# Default: true
enabled = true

# Default model downgrade chain used when action is "downgrade".
default_downgrade_chain = [
    "claude-fable-5",
    "claude-opus-5",
    "claude-sonnet-5",
    "claude-haiku-4-5",
]

# Percentage of budget at which downgrade kicks in.
# Default: 80
downgrade_threshold_pct = 80

# Alert thresholds (percentage of budget used).
# Default: [50, 80, 100]
alert_thresholds_pct = [50, 80, 100]

[gate.global_budget]
# Global budget across all agents.
limit_usd = 0
period = "monthly"

[gate.default_budget]
# Default budget applied to agents not explicitly configured.
limit_usd = 0
period = "daily"
action_on_exceed = "alert"

[gate.runaway]
# Runaway detection: if an agent makes more than N requests in M seconds, kill it.
enabled = true
max_requests = 100
window_seconds = 60

[gate.identification]
# Agent identification method.
# "header" uses X-Oberwatch-Agent.
# "api_key" maps API key prefixes with [[gate.api_key_map]].
# "source_ip" maps source IPs to agents.
method = "header"

# Uncomment to define per-agent budget overrides.
# [[gate.agents]]
# name = "my-agent"
# limit_usd = 10.00
# period = "daily"
# action_on_exceed = "downgrade"
# downgrade_chain = ["claude-sonnet-5", "claude-haiku-4-5"]

# Uncomment when method = "api_key".
# [[gate.api_key_map]]
# api_key_prefix = "sk-proj-abc"
# agent = "my-agent"

# -----------------------------------------------------------------------------
# Alerts
# -----------------------------------------------------------------------------
[alerts]
# Webhook URL for generic HTTP POST alerts.
webhook_url = ""

# Slack webhook URL.
slack_webhook_url = ""

[alerts.email]
# Email alerts via SMTP.
enabled = false
smtp_host = ""
smtp_port = 587
smtp_user = ""
smtp_password = ""
from = ""
to = []

# -----------------------------------------------------------------------------
# Trace (Decision Debugger) Settings
# -----------------------------------------------------------------------------
[trace]
# Enable trace collection.
# Default: true
enabled = true

# Capture request/response content.
# WARNING: This stores potentially sensitive data.
capture_content = false

# Maximum number of traces to keep in memory.
memory_buffer_size = 1000

# Trace retention period.
retention = "168h"

# Storage backend: "memory", "sqlite"
storage = "sqlite"

# SQLite database path, used when storage = "sqlite".
sqlite_path = "./oberwatch.db"

# Close traces after this period of inactivity.
trace_timeout = "30s"

# -----------------------------------------------------------------------------
# Test (Behavioral Test Harness) Settings
# -----------------------------------------------------------------------------
[test]
# Directory containing YAML scenario files.
scenarios_dir = "./scenarios"

# Maximum parallel test execution.
concurrency = 4

# Default timeout per scenario.
timeout = "30s"

[test.judge]
# Model to use for LLM-as-judge assertions.
model = "claude-haiku-4-5"

# Provider for the judge model.
provider = "anthropic"

# API key for the judge model.
api_key = ""

# -----------------------------------------------------------------------------
# Model Pricing
# Prices in USD per 1 million tokens.
#
# Only models with a single published rate are listed. A model billed at a
# higher rate above a context threshold is left out, because one rate pair
# would undercount long requests. Add such a model here yourself with the rate
# that applies to your context window.
# -----------------------------------------------------------------------------
[[pricing]]
model = "gpt-5.6-sol"
provider = "openai"
input_per_million = 4.00
output_per_million = 20.00

[[pricing]]
model = "gpt-5.6-terra"
provider = "openai"
input_per_million = 2.00
output_per_million = 12.00

[[pricing]]
model = "gpt-5.6-luna"
provider = "openai"
input_per_million = 0.20
output_per_million = 1.20

[[pricing]]
model = "claude-fable-5"
provider = "anthropic"
input_per_million = 10.00
output_per_million = 50.00

[[pricing]]
model = "claude-opus-5"
provider = "anthropic"
input_per_million = 5.00
output_per_million = 25.00

[[pricing]]
model = "claude-sonnet-5"
provider = "anthropic"
input_per_million = 2.00
output_per_million = 10.00

[[pricing]]
model = "claude-haiku-4-5"
provider = "anthropic"
input_per_million = 1.00
output_per_million = 5.00

[[pricing]]
model = "gemini-3.7-flash"
provider = "google"
input_per_million = 0.75
output_per_million = 3.75

[[pricing]]
model = "gemini-3.5-flash-lite"
provider = "google"
input_per_million = 0.30
output_per_million = 2.50
`

// DefaultInitOutput is where `oberwatch init` writes when --output is omitted.
const DefaultInitOutput = "./oberwatch.toml"

// GenerateStarter writes StarterTOML to the requested path without overwriting an existing file.
func GenerateStarter(path string) error {
	return WriteStarter(path, false)
}

// WriteStarter writes StarterTOML to path, creating parent directories as
// needed. An existing file is only replaced when force is true; otherwise the
// file is left untouched and an error is returned.
//
// Existence is checked with Lstat so a symlink counts as an existing entry:
// without --force the link is refused rather than followed, which keeps init
// from writing through a dangling link to some other path.
func WriteStarter(path string, force bool) error {
	if path == "" {
		return fmt.Errorf("output path must not be empty")
	}

	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			// Report the target's type when the link resolves, so a symlinked
			// directory still gets the clearer message below.
			if target, targetErr := os.Stat(path); targetErr == nil {
				info = target
			}
		}
		if info.IsDir() {
			return fmt.Errorf("output path %q is a directory, want a file path", path)
		}
		if !force {
			return errRefuseOverwrite(path)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %q: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory for %q: %w", path, err)
	}

	// O_EXCL without --force closes the gap between the check above and the
	// write, so a file that appears in between is never truncated.
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if !force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}
	file, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		if !force && errors.Is(err, os.ErrExist) {
			return errRefuseOverwrite(path)
		}
		return fmt.Errorf("write starter config %q: %w", path, err)
	}
	if _, err := file.WriteString(StarterTOML); err != nil {
		_ = file.Close()
		return fmt.Errorf("write starter config %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("write starter config %q: %w", path, err)
	}

	return nil
}

func errRefuseOverwrite(path string) error {
	return fmt.Errorf("refusing to overwrite existing file %q (use --force to replace it)", path)
}

// InitSuccessMessage is the stdout text printed after `oberwatch init` writes path.
func InitSuccessMessage(path string) string {
	return fmt.Sprintf("wrote starter config to %s\nnext: oberwatch serve --config %s\n", path, path)
}

// ValidSuccessMessage is the exact stdout text printed when `oberwatch validate` succeeds.
func ValidSuccessMessage(path string) string {
	return fmt.Sprintf("config %s is valid\n", path)
}
