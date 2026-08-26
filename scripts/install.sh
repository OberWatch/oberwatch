#!/bin/sh
set -eu

REPO_OWNER="OberWatch"
REPO_NAME="oberwatch"
LATEST_RELEASE_URL="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/latest"
RAW_BASE_URL="https://raw.githubusercontent.com/${REPO_OWNER}/${REPO_NAME}/main"
INSTALL_PATH="/usr/local/bin/oberwatch"
SERVICE_NAME="oberwatch"
LINUX_SERVICE_USER="oberwatch"
LINUX_SERVICE_HOME="/home/${LINUX_SERVICE_USER}"
LINUX_STATE_DIR="${LINUX_SERVICE_HOME}/.oberwatch"
HEALTH_URL="http://localhost:8080/_oberwatch/api/v1/health"

print_banner() {
  cat <<'BANNER'
 ::::::::  :::::::::  :::::::::: :::::::::  :::       :::     ::: ::::::::::: ::::::::  :::    ::: 
:+:    :+: :+:    :+: :+:        :+:    :+: :+:       :+:   :+: :+:   :+:    :+:    :+: :+:    :+: 
+:+    +:+ +:+    +:+ +:+        +:+    +:+ +:+       +:+  +:+   +:+  +:+    +:+        +:+    +:+ 
+#+    +:+ +#++:++#+  +#++:++#   +#++:++#:  +#+  +:+  +#+ +#++:++#++: +#+    +#+        +#++:++#++ 
+#+    +#+ +#+    +#+ +#+        +#+    +#+ +#+ +#+#+ +#+ +#+     +#+ +#+    +#+        +#+    +#+ 
#+#    #+# #+#    #+# #+#        #+#    #+#  #+#+# #+#+#  #+#     #+# #+#    #+#    #+# #+#    #+# 
 ########  #########  ########## ###    ###   ###   ###   ###     ### ###     ########  ###    ### 
BANNER
  printf '\n'
}

log() {
  printf '%s\n' "$*"
}

fail() {
  printf 'Error: %s\n' "$*" >&2
  exit 1
}

print_usage() {
  printf '%s\n' \
    'Usage: install.sh [--help]' \
    '' \
    'Install or upgrade Oberwatch on Linux.'
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

sudo_cmd() {
  if [ "$(id -u)" -eq 0 ]; then
    "$@"
  else
    need_cmd sudo
    sudo "$@"
  fi
}

prompt_yes_no() {
  prompt_yes_no_prompt="$1"
  prompt_yes_no_reply=""
  prompt_yes_no_input_path="${2:-/dev/tty}"
  printf '%s' "$prompt_yes_no_prompt"
  if ! IFS= read -r prompt_yes_no_reply <"${prompt_yes_no_input_path}"; then
    printf '\nRefusing upgrade: cannot confirm upgrade without a terminal.\n' >&2
    return 2
  fi
  case "${prompt_yes_no_reply}" in
    y|Y|yes|YES)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

resolve_user_home() {
  if [ -n "${SUDO_USER:-}" ] && [ "${SUDO_USER}" != "root" ]; then
    if [ -d "/home/${SUDO_USER}" ]; then
      printf '%s\n' "/home/${SUDO_USER}"
      return
    fi
    if [ -d "/Users/${SUDO_USER}" ]; then
      printf '%s\n' "/Users/${SUDO_USER}"
      return
    fi
  fi
  printf '%s\n' "${HOME}"
}

write_default_config() {
  write_default_config_target="$1"
  cat >"${write_default_config_target}" <<'CONFIG'
# =============================================================================
# Oberwatch Example Configuration
# =============================================================================
# Copy this file to `oberwatch.toml` and adjust values for your environment.
# Every option below includes a comment explaining what it controls.

# -----------------------------------------------------------------------------
# Server Settings
# -----------------------------------------------------------------------------
[server]
# TCP port the Oberwatch HTTP server listens on.
# Env: OBERWATCH_SERVER__PORT
port = 8080

# Bind address for incoming HTTP connections.
# Use 127.0.0.1 for local-only access.
# Env: OBERWATCH_SERVER__HOST
host = "0.0.0.0"

# Bearer token required for management API and dashboard endpoints.
# Keep this secret in production.
# Env: OBERWATCH_SERVER__ADMIN_TOKEN
admin_token = "change-me-admin-token"

# Enable or disable the embedded dashboard.
# Env: OBERWATCH_SERVER__DASHBOARD
dashboard = true

# Structured log level.
# Valid values: debug, info, warn, error
# Env: OBERWATCH_SERVER__LOG_LEVEL
log_level = "info"

# Log output format.
# Valid values: text, json
# Env: OBERWATCH_SERVER__LOG_FORMAT
log_format = "text"

# Optional TLS certificate path for HTTPS mode.
# Leave empty to run plain HTTP.
# Env: OBERWATCH_SERVER__TLS_CERT
tls_cert = ""

# Optional TLS private key path for HTTPS mode.
# Must be set together with tls_cert.
# Env: OBERWATCH_SERVER__TLS_KEY
tls_key = ""

# -----------------------------------------------------------------------------
# Upstream Provider Settings
# -----------------------------------------------------------------------------
[upstream]
# Default provider used when path-based provider detection is ambiguous.
# Valid values: openai, anthropic, ollama, custom
# Env: OBERWATCH_UPSTREAM__DEFAULT_PROVIDER
default_provider = "openai"

# Upstream request timeout (Go duration string).
# Env: OBERWATCH_UPSTREAM__TIMEOUT
timeout = "120s"

[upstream.openai]
# Base URL for OpenAI-compatible endpoints.
# Env: OBERWATCH_UPSTREAM__OPENAI__BASE_URL
base_url = "https://api.openai.com"

[upstream.anthropic]
# Base URL for Anthropic API.
# Env: OBERWATCH_UPSTREAM__ANTHROPIC__BASE_URL
base_url = "https://api.anthropic.com"

[upstream.ollama]
# Base URL for local or remote Ollama server.
# Env: OBERWATCH_UPSTREAM__OLLAMA__BASE_URL
base_url = "http://localhost:11434"

[upstream.custom]
# Base URL for an additional OpenAI-compatible provider.
# Leave empty when unused.
# Env: OBERWATCH_UPSTREAM__CUSTOM__BASE_URL
base_url = ""

# -----------------------------------------------------------------------------
# Gate (Budget / Cost Governor) Settings
# -----------------------------------------------------------------------------
[gate]
# Enable budget enforcement and cost tracking.
# Env: OBERWATCH_GATE__ENABLED
enabled = true

# Default model downgrade path when action_on_exceed is "downgrade".
default_downgrade_chain = [
  "claude-opus-4-6",
  "claude-sonnet-4-6",
  "claude-haiku-4-5",
]

# Spend percentage at which downgrade behavior begins.
downgrade_threshold_pct = 80

# Alert thresholds as percentages of budget used.
alert_thresholds_pct = [50, 80, 100]

[gate.global_budget]
# Global shared budget across all agents.
# Use 0 for "unlimited".
limit_usd = 500

# Reset period for global budget.
# Valid values: hourly, daily, weekly, monthly
period = "monthly"

[gate.default_budget]
# Default per-agent budget if no explicit [[gate.agents]] override exists.
# Use 0 for "unlimited".
limit_usd = 25

# Reset period for the default per-agent budget.
# Valid values: hourly, daily, weekly, monthly
period = "daily"

# Action when budget is exceeded.
# Valid values: reject, downgrade, alert, kill
action_on_exceed = "alert"

[gate.runaway]
# Enable high-frequency runaway request detection.
enabled = true

# Maximum requests allowed in the window before kill behavior triggers.
max_requests = 100

# Window size in seconds for runaway detection.
window_seconds = 60

[gate.identification]
# Agent identity source.
# Valid values: header, api_key, source_ip
method = "header"

# Explicit per-agent policies (two example agents configured).
[[gate.agents]]
# Stable agent name as seen in X-Oberwatch-Agent header or key mapping.
name = "email-agent"

# Agent-specific budget limit in USD for the chosen period.
limit_usd = 10.00

# Budget reset period for this agent.
period = "daily"

# Enforcement action for this agent.
action_on_exceed = "downgrade"

# Agent-specific downgrade chain override.
downgrade_chain = ["claude-sonnet-4-6", "claude-haiku-4-5"]

[[gate.agents]]
name = "finance-agent"
limit_usd = 50.00
period = "weekly"
action_on_exceed = "reject"
downgrade_chain = ["gpt-4.1", "gpt-4.1-mini"]

# Optional API key prefix-to-agent mapping when identification.method = "api_key".
# [[gate.api_key_map]]
# api_key_prefix = "sk-proj-email"
# agent = "email-agent"

# -----------------------------------------------------------------------------
# Alert Delivery Settings
# -----------------------------------------------------------------------------
[alerts]
# Generic webhook URL for alert POSTs.
webhook_url = ""

# Slack incoming webhook URL for alert notifications.
slack_webhook_url = ""

[alerts.email]
# Enable SMTP-based email alerting.
enabled = false

# SMTP server hostname.
smtp_host = ""

# SMTP server port.
smtp_port = 587

# SMTP username.
smtp_user = ""

# SMTP password or app-specific token.
smtp_password = ""

# Sender email address.
from = ""

# Recipient email addresses.
to = []

# -----------------------------------------------------------------------------
# Trace Settings
# -----------------------------------------------------------------------------
[trace]
# Enable trace capture and storage.
enabled = true

# Capture prompt/response payload content in traces.
# Disable when handling sensitive data.
capture_content = false

# Max in-memory trace buffer size (used by memory storage mode).
memory_buffer_size = 1000

# Retention duration for persisted traces (Go duration string).
retention = "168h"

# Trace storage backend.
# Valid values: memory, sqlite
storage = "sqlite"

# SQLite file path when storage = "sqlite".
sqlite_path = "./oberwatch.db"

# Mark active traces as timed out after this idle duration.
trace_timeout = "30s"

# -----------------------------------------------------------------------------
# Behavioral Test Harness Settings
# -----------------------------------------------------------------------------
[test]
# Directory containing YAML scenario files.
scenarios_dir = "./scenarios"

# Maximum number of concurrent test scenarios.
concurrency = 4

# Default timeout per scenario (Go duration string).
timeout = "30s"
CONFIG
}

detect_platform() {
  detect_platform_os=""
  detect_platform_arch=""
  detect_platform_uname_os="$(uname -s)"
  detect_platform_uname_arch="$(uname -m)"

  case "${detect_platform_uname_os}" in
    Linux)
      detect_platform_os="linux"
      ;;
    Darwin)
      detect_platform_os="darwin"
      ;;
    *)
      fail "unsupported operating system: ${detect_platform_uname_os}. Oberwatch installer currently supports Linux only."
      ;;
  esac

  case "${detect_platform_uname_arch}" in
    x86_64|amd64)
      detect_platform_arch="amd64"
      ;;
    arm64|aarch64)
      detect_platform_arch="arm64"
      ;;
    i386|i686|x86)
      fail "unsupported architecture: ${detect_platform_uname_arch}. 32-bit platforms are not supported."
      ;;
    *)
      fail "unsupported architecture: ${detect_platform_uname_arch}. Supported architectures are amd64 and arm64."
      ;;
  esac

  RELEASE_OS="${detect_platform_os}"
  RELEASE_ARCH="${detect_platform_arch}"
}

validate_release_tag() {
  validate_release_tag_value="$1"
  case "${validate_release_tag_value}" in
    v*) ;;
    *) return 1 ;;
  esac

  validate_release_tag_version="${validate_release_tag_value#v}"
  validate_release_tag_core="${validate_release_tag_version%%-*}"
  if [ "${validate_release_tag_core}" != "${validate_release_tag_version}" ]; then
    validate_release_tag_prerelease="${validate_release_tag_version#*-}"
    while :; do
      validate_release_tag_identifier="${validate_release_tag_prerelease%%.*}"
      case "${validate_release_tag_identifier}" in
        ''|*[!A-Za-z0-9-]*) return 1 ;;
      esac
      case "${validate_release_tag_identifier}" in
        *[!0-9]*) ;;
        0|[1-9]*) ;;
        *) return 1 ;;
      esac
      [ "${validate_release_tag_identifier}" != "${validate_release_tag_prerelease}" ] || break
      validate_release_tag_prerelease="${validate_release_tag_prerelease#*.}"
    done
  fi

  validate_release_tag_major="${validate_release_tag_core%%.*}"
  validate_release_tag_remainder="${validate_release_tag_core#*.}"
  [ "${validate_release_tag_remainder}" != "${validate_release_tag_core}" ] || return 1
  validate_release_tag_minor="${validate_release_tag_remainder%%.*}"
  validate_release_tag_patch="${validate_release_tag_remainder#*.}"
  [ "${validate_release_tag_patch}" != "${validate_release_tag_remainder}" ] || return 1
  case "${validate_release_tag_patch}" in
    *.*) return 1 ;;
  esac
  for validate_release_tag_part in \
    "${validate_release_tag_major}" \
    "${validate_release_tag_minor}" \
    "${validate_release_tag_patch}"
  do
    case "${validate_release_tag_part}" in
      ''|*[!0-9]*|0[0-9]*) return 1 ;;
    esac
  done
  return 0
}

fetch_latest_tag() {
  fetch_latest_tag_effective_url="$(curl -fsSL -o /dev/null -w '%{url_effective}\n' "${LATEST_RELEASE_URL}")" || fail "failed to resolve latest GitHub release at ${LATEST_RELEASE_URL}"
  fetch_latest_tag_prefix="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/tag/"
  case "${fetch_latest_tag_effective_url}" in
    "${fetch_latest_tag_prefix}"*)
      fetch_latest_tag_value="${fetch_latest_tag_effective_url#"${fetch_latest_tag_prefix}"}"
      ;;
    *)
      fail "unexpected latest release URL from GitHub: ${fetch_latest_tag_effective_url}"
      ;;
  esac
  case "${fetch_latest_tag_value}" in
    *[/?#]*)
      fail "unexpected latest release URL from GitHub: ${fetch_latest_tag_effective_url}"
      ;;
  esac
  validate_release_tag "${fetch_latest_tag_value}" || fail "unsafe release tag received from GitHub: ${fetch_latest_tag_value}"
  LATEST_TAG="${fetch_latest_tag_value}"
}

download_binary() {
  download_binary_version="${LATEST_TAG#v}"
  download_binary_asset_name="oberwatch_${download_binary_version}_${RELEASE_OS}_${RELEASE_ARCH}.tar.gz"
  download_binary_url="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/download/${LATEST_TAG}/${download_binary_asset_name}"
  download_binary_archive="${TMP_DIR}/${download_binary_asset_name}"

  log "Downloading ${download_binary_asset_name} from ${LATEST_TAG}..."
  curl -fsSL "${download_binary_url}" -o "${download_binary_archive}" || fail "failed to download ${download_binary_url}"
  [ -s "${download_binary_archive}" ] || fail "downloaded archive is empty: ${download_binary_archive}"

  log "Extracting binary..."
  tar -xzf "${download_binary_archive}" -C "${TMP_DIR}" oberwatch || fail "failed to extract binary from ${download_binary_archive}"
  download_binary_path="${TMP_DIR}/oberwatch"
  [ -f "${download_binary_path}" ] || fail "binary not found after extraction: ${download_binary_path}"
  chmod +x "${download_binary_path}"

  DOWNLOADED_BINARY="${download_binary_path}"
}

install_binary() {
  sudo_cmd install -m 0755 "${DOWNLOADED_BINARY}" "${INSTALL_PATH}"
  if ! "${INSTALL_PATH}" version >/dev/null 2>&1; then
    fail "installed binary verification failed: ${INSTALL_PATH} version"
  fi
}

ensure_user_state_dirs() {
  mkdir -p "${USER_STATE_DIR}/data"
  if [ ! -f "${USER_CONFIG_PATH}" ]; then
    write_default_config "${USER_CONFIG_PATH}"
    log "Generated default config at ${USER_CONFIG_PATH}"
  else
    log "Existing config found, keeping it"
  fi
}

setup_linux_service_user() {
  if ! id -u "${LINUX_SERVICE_USER}" >/dev/null 2>&1; then
    setup_linux_service_user_nologin_shell="$(command -v nologin || true)"
    if [ -z "${setup_linux_service_user_nologin_shell}" ]; then
      if [ -x /usr/sbin/nologin ]; then
        setup_linux_service_user_nologin_shell="/usr/sbin/nologin"
      elif [ -x /sbin/nologin ]; then
        setup_linux_service_user_nologin_shell="/sbin/nologin"
      else
        setup_linux_service_user_nologin_shell="/bin/false"
      fi
    fi
    sudo_cmd useradd --system --no-create-home --shell "${setup_linux_service_user_nologin_shell}" "${LINUX_SERVICE_USER}"
  fi
}

sync_linux_service_state() {
  sudo_cmd mkdir -p "${LINUX_STATE_DIR}/data"
  sudo_cmd cp "${USER_CONFIG_PATH}" "${LINUX_STATE_DIR}/oberwatch.toml"
  if [ -d "${USER_STATE_DIR}/data" ]; then
    sudo_cmd sh -c 'cp -R "$1/data/." "$2/data/" 2>/dev/null || true' sh "${USER_STATE_DIR}" "${LINUX_STATE_DIR}"
  fi
  sudo_cmd chown -R "${LINUX_SERVICE_USER}:${LINUX_SERVICE_USER}" "${LINUX_SERVICE_HOME}" "${LINUX_STATE_DIR}"
}

write_systemd_service() {
  write_systemd_service_path="/etc/systemd/system/${SERVICE_NAME}.service"
  sudo_cmd tee "${write_systemd_service_path}" >/dev/null <<SERVICE
[Unit]
Description=Oberwatch - AI Agent Proxy & Observability
After=network.target

[Service]
Type=simple
User=${LINUX_SERVICE_USER}
Group=${LINUX_SERVICE_USER}
ExecStart=${INSTALL_PATH} serve --config ${LINUX_STATE_DIR}/oberwatch.toml
Restart=always
RestartSec=5
WorkingDirectory=${LINUX_STATE_DIR}
Environment=OBERWATCH_TRACE__SQLITE_PATH=${LINUX_STATE_DIR}/data/oberwatch.db

[Install]
WantedBy=multi-user.target
SERVICE
}

start_linux_service() {
  need_cmd systemctl
  sudo_cmd systemctl daemon-reload
  sudo_cmd systemctl enable "${SERVICE_NAME}"
  if sudo_cmd systemctl is-active --quiet "${SERVICE_NAME}"; then
    sudo_cmd systemctl restart "${SERVICE_NAME}"
  else
    sudo_cmd systemctl start "${SERVICE_NAME}"
  fi
}

wait_for_health() {
  wait_for_health_attempt=1
  while [ "${wait_for_health_attempt}" -le 15 ]; do
    if curl -fsSL "${HEALTH_URL}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
    wait_for_health_attempt=$((wait_for_health_attempt + 1))
  done
  return 1
}

print_success_linux() {
  cat <<SUCCESS

✓ Oberwatch is installed and running!

→ Dashboard:  http://localhost:8080
→ Proxy URL:  http://localhost:8080
→ Config:     ${LINUX_STATE_DIR}/oberwatch.toml
→ Data:       ${LINUX_STATE_DIR}/data/
→ Logs:       sudo journalctl -u ${SERVICE_NAME} -f

Open the dashboard to complete setup.

Quick start:
  1. Open http://localhost:8080 in your browser
  2. Create your admin account
  3. Point your AI agents at http://localhost:8080 instead of api.openai.com

Other install methods:
  Docker:         docker run -d -p 8080:8080 -v oberwatch-data:/data ghcr.io/oberwatch/oberwatch:latest
  Docker Compose: wget https://raw.githubusercontent.com/OberWatch/oberwatch/main/docker-compose.yml
  From source:    git clone https://github.com/OberWatch/oberwatch && cd oberwatch && make build
SUCCESS
}

print_success_macos() {
  cat <<SUCCESS

✓ Oberwatch is installed!

→ Binary:     ${INSTALL_PATH}
→ Config:     ${USER_CONFIG_PATH}
→ Data:       ${USER_STATE_DIR}/data/

To start Oberwatch, run: oberwatch serve
To run in background: nohup oberwatch serve &

Open the dashboard to complete setup.

Quick start:
  1. Run oberwatch serve
  2. Open http://localhost:8080 in your browser
  3. Create your admin account
  4. Point your AI agents at http://localhost:8080 instead of api.openai.com

Other install methods:
  Docker:         docker run -d -p 8080:8080 -v oberwatch-data:/data ghcr.io/oberwatch/oberwatch:latest
  Docker Compose: wget https://raw.githubusercontent.com/OberWatch/oberwatch/main/docker-compose.yml
  From source:    git clone https://github.com/OberWatch/oberwatch && cd oberwatch && make build
SUCCESS
}

main() {
  case "${1:-}" in
    -h|--help)
      print_usage
      return 0
      ;;
  esac

  need_cmd curl
  need_cmd chmod
  need_cmd install
  need_cmd mktemp
  need_cmd uname

  print_banner
  detect_platform

  USER_HOME="$(resolve_user_home)"
  USER_STATE_DIR="${USER_HOME}/.oberwatch"
  USER_CONFIG_PATH="${USER_STATE_DIR}/oberwatch.toml"
  TMP_DIR="$(mktemp -d)"
  trap 'rm -rf "${TMP_DIR}"' 0

  if [ -x "${INSTALL_PATH}" ]; then
    if prompt_yes_no "Oberwatch is already installed. Upgrade to latest? (y/N) "; then
      log "Upgrading existing installation..."
    else
      main_prompt_status=$?
      case "${main_prompt_status}" in
        1) return 0 ;;
        *) return "${main_prompt_status}" ;;
      esac
    fi
  fi

  if [ "${RELEASE_OS}" = "darwin" ]; then
    fail "macOS binaries are not yet available. Install via Docker instead:\n  docker run -d -p 8080:8080 -v oberwatch-data:/data ghcr.io/oberwatch/oberwatch:latest"
  fi

  fetch_latest_tag
  download_binary
  install_binary
  ensure_user_state_dirs

  if [ "${RELEASE_OS}" = "linux" ]; then
    setup_linux_service_user
    sync_linux_service_state
    write_systemd_service
    start_linux_service

    if wait_for_health; then
      print_success_linux
    else
      log "Oberwatch failed to start. Check logs: sudo journalctl -u ${SERVICE_NAME} --no-pager -n 50"
      exit 1
    fi
  else
    print_success_macos
  fi
}

main "$@"
