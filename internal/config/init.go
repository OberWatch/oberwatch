package config

import (
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

# Admin token for the management API and dashboard.
# REQUIRED in production. If not set, management API is disabled.
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
# Options: "openai", "anthropic", "ollama", "custom"
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
    "claude-opus-4-6",
    "claude-sonnet-4-6",
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
# name = "email-agent"
# limit_usd = 10.00
# period = "daily"
# action_on_exceed = "downgrade"
# downgrade_chain = ["claude-sonnet-4-6", "claude-haiku-4-5"]

# Uncomment when method = "api_key".
# [[gate.api_key_map]]
# api_key_prefix = "sk-proj-abc"
# agent = "email-agent"

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
# -----------------------------------------------------------------------------
[[pricing]]
model = "gpt-4o"
provider = "openai"
input_per_million = 2.50
output_per_million = 10.00

[[pricing]]
model = "gpt-4o-mini"
provider = "openai"
input_per_million = 0.15
output_per_million = 0.60

[[pricing]]
model = "gpt-4.1"
provider = "openai"
input_per_million = 2.00
output_per_million = 8.00

[[pricing]]
model = "gpt-4.1-mini"
provider = "openai"
input_per_million = 0.40
output_per_million = 1.60

[[pricing]]
model = "claude-opus-4-6"
provider = "anthropic"
input_per_million = 5.00
output_per_million = 25.00

[[pricing]]
model = "claude-sonnet-4-6"
provider = "anthropic"
input_per_million = 3.00
output_per_million = 15.00

[[pricing]]
model = "claude-haiku-4-5"
provider = "anthropic"
input_per_million = 1.00
output_per_million = 5.00

[[pricing]]
model = "gemini-2.5-pro"
provider = "google"
input_per_million = 1.25
output_per_million = 10.00

[[pricing]]
model = "gemini-2.5-flash"
provider = "google"
input_per_million = 0.15
output_per_million = 0.60
`

// GenerateStarter writes StarterTOML to the requested path without overwriting an existing file.
func GenerateStarter(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("refusing to overwrite existing file %q", path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %q: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory for %q: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(StarterTOML), 0o644); err != nil {
		return fmt.Errorf("write starter config %q: %w", path, err)
	}

	return nil
}
