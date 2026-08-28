#!/usr/bin/env bash
set -euo pipefail

SERVICE_NAME="oberwatch"
INSTALL_PATH="/usr/local/bin/oberwatch"
SYSTEMD_UNIT="/etc/systemd/system/${SERVICE_NAME}.service"
SYSTEM_USER="oberwatch"
SYSTEM_HOME="/home/${SYSTEM_USER}/.oberwatch"
UPGRADE_SERVICE_NAME="oberwatch-upgrade"
UPGRADE_UNITS=(
  "/etc/systemd/system/${UPGRADE_SERVICE_NAME}.path"
  "/etc/systemd/system/${UPGRADE_SERVICE_NAME}.service"
)
UPGRADE_STATE_ROOT="/var/lib/oberwatch"

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    printf 'Error: required command not found: %s\n' "$1" >&2
    exit 1
  }
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
  local prompt="$1"
  local reply
  printf '%s' "$prompt"
  read -r reply || true
  case "${reply}" in
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

main() {
  local os user_home user_state_dir
  os="$(uname -s)"
  user_home="$(resolve_user_home)"
  user_state_dir="${user_home}/.oberwatch"

  if [ "${os}" = "Linux" ] && command -v systemctl >/dev/null 2>&1; then
    if sudo_cmd systemctl list-unit-files | grep -q "^${SERVICE_NAME}\.service"; then
      sudo_cmd systemctl stop "${SERVICE_NAME}" || true
      sudo_cmd systemctl disable "${SERVICE_NAME}" || true
    fi
    if sudo_cmd systemctl list-unit-files | grep -q "^${UPGRADE_SERVICE_NAME}\.path"; then
      sudo_cmd systemctl stop "${UPGRADE_SERVICE_NAME}.path" || true
      sudo_cmd systemctl disable "${UPGRADE_SERVICE_NAME}.path" || true
    fi
    if [ -f "${SYSTEMD_UNIT}" ]; then
      sudo_cmd rm -f "${SYSTEMD_UNIT}"
    fi
    for unit in "${UPGRADE_UNITS[@]}"; do
      if [ -f "${unit}" ]; then
        sudo_cmd rm -f "${unit}"
      fi
    done
    sudo_cmd systemctl daemon-reload || true
  fi

  if [ -f "${INSTALL_PATH}" ]; then
    sudo_cmd rm -f "${INSTALL_PATH}"
  fi

  # The upgrade handoff directory only ever holds a staged release archive and
  # the request and result files, never config or data, so it goes with the
  # binary rather than waiting on the data prompt below.
  if [ -d "${UPGRADE_STATE_ROOT}" ]; then
    sudo_cmd rm -rf "${UPGRADE_STATE_ROOT}"
  fi
  if [ -f "${INSTALL_PATH}.previous" ]; then
    sudo_cmd rm -f "${INSTALL_PATH}.previous"
  fi

  if prompt_yes_no "Remove all data and config? This cannot be undone. (y/N) "; then
    rm -rf "${user_state_dir}"
    if [ -d "${SYSTEM_HOME}" ]; then
      sudo_cmd rm -rf "${SYSTEM_HOME}"
    fi
  else
    printf 'Config and data preserved at %s/\n' "${user_state_dir}"
  fi

  if [ "${os}" = "Linux" ] && id -u "${SYSTEM_USER}" >/dev/null 2>&1; then
    sudo_cmd userdel "${SYSTEM_USER}" || true
  fi

  printf 'Oberwatch has been uninstalled.\n'
}

main "$@"
