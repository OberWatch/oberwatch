#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BIN_DIR="${ROOT_DIR}/.tools/bin"
GOLANGCI_LINT_VERSION="${GOLANGCI_LINT_VERSION:-2.11.4}"
GOLANGCI_LINT_BASE_URL="${GOLANGCI_LINT_BASE_URL:-https://github.com/golangci/golangci-lint/releases/download}"

mkdir -p "${BIN_DIR}"

if ! command -v go >/dev/null 2>&1; then
	echo "error: go is required but was not found in PATH" >&2
	exit 1
fi

go_version="$(go env GOVERSION 2>/dev/null || true)"
case "${go_version}" in
go1.2[6-9]* | go1.[3-9][0-9]*)
	;;
*)
	echo "error: Go 1.26+ is required (found: ${go_version:-unknown})" >&2
	exit 1
	;;
esac

install_golangci_lint() {
	local os arch archive_url tmp_dir archive_path extracted_dir
	os="$(uname -s | tr '[:upper:]' '[:lower:]')"
	arch="$(uname -m)"

	case "${arch}" in
	x86_64)
		arch="amd64"
		;;
	aarch64 | arm64)
		arch="arm64"
		;;
	*)
		echo "error: unsupported architecture '${arch}' for golangci-lint installer" >&2
		exit 1
		;;
	esac

	if ! command -v curl >/dev/null 2>&1; then
		echo "error: curl is required to install golangci-lint" >&2
		exit 1
	fi

	tmp_dir="$(mktemp -d)"
	trap 'rm -rf "${tmp_dir}"' RETURN
	archive_path="${tmp_dir}/golangci-lint.tar.gz"
	archive_url="${GOLANGCI_LINT_BASE_URL}/v${GOLANGCI_LINT_VERSION}/golangci-lint-${GOLANGCI_LINT_VERSION}-${os}-${arch}.tar.gz"

	echo "installing golangci-lint v${GOLANGCI_LINT_VERSION}..."
	if ! curl -fsSL "${archive_url}" -o "${archive_path}"; then
		echo "error: failed to download golangci-lint from ${archive_url}" >&2
		echo "hint: set GOLANGCI_LINT_BASE_URL if you use an internal mirror" >&2
		exit 1
	fi
	tar -xzf "${archive_path}" -C "${tmp_dir}"
	extracted_dir="${tmp_dir}/golangci-lint-${GOLANGCI_LINT_VERSION}-${os}-${arch}"
	install -m 0755 "${extracted_dir}/golangci-lint" "${BIN_DIR}/golangci-lint"
}

if [[ -x "${BIN_DIR}/golangci-lint" ]]; then
	current_version="$("${BIN_DIR}/golangci-lint" version --short 2>/dev/null || true)"
	if [[ "${current_version}" == "${GOLANGCI_LINT_VERSION}" ]]; then
		echo "golangci-lint v${GOLANGCI_LINT_VERSION} already installed at ${BIN_DIR}/golangci-lint"
		exit 0
	fi
fi

install_golangci_lint
echo "installed tools in ${BIN_DIR}"
