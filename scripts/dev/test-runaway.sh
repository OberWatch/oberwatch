#!/usr/bin/env bash
# End-to-end check of runaway detection against a mock upstream.
#
# Starts a mock OpenAI-compatible upstream that also acts as the alert
# webhook, starts oberwatch with a small runaway window, then proves:
#   - max_requests inside the window pass through to the upstream
#   - request max+1 returns 429 with error.code=agent_killed
#   - killed requests never reach the upstream
#   - exactly one runaway_detected and one agent_killed alert are stored
#     and delivered to the webhook
#   - the kill stays sticky after the window has passed
#   - POST /budgets/{agent}/enable restores traffic without replaying alerts
#
# Usage: scripts/dev/test-runaway.sh [path-to-oberwatch-binary]
# Without an argument the script builds ./cmd/oberwatch into a temp dir. The
# mock upstream is always built from source, so a Go toolchain is required.
#
# Everything runs on loopback, needs no provider credentials, is bounded by
# RUNAWAY_TEST_TIMEOUT seconds, and cleans up processes and files on exit.
set -euo pipefail

# Resolve a caller-supplied binary against the caller's directory before
# moving to the repository root.
BINARY_ARG="${1:-}"
case "$BINARY_ARG" in
  "" | /*) ;;
  *) BINARY_ARG="$(pwd)/$BINARY_ARG" ;;
esac

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

# OBERWATCH_* variables are applied as config overrides, so any left in the
# caller's environment would change what this contract proves.
while IFS='=' read -r name _; do
  case "$name" in
    OBERWATCH_*) unset "$name" ;;
  esac
done < <(env)

PORT="${RUNAWAY_TEST_PORT:-18296}"
MOCK_PORT="${RUNAWAY_TEST_MOCK_PORT:-18297}"
TIMEOUT_SECONDS="${RUNAWAY_TEST_TIMEOUT:-120}"
MAX_REQUESTS=3
WINDOW_SECONDS=3
AGENT="runaway-agent"
BYSTANDER="calm-agent"
ADMIN_USER="runaway-admin"
ADMIN_PASS="runaway-password"

BASE="http://127.0.0.1:${PORT}"
API="${BASE}/_oberwatch/api/v1"
MOCK="http://127.0.0.1:${MOCK_PORT}"

if [ -z "${RUNAWAY_TEST_CHILD:-}" ]; then
  if command -v timeout >/dev/null 2>&1; then
    # Re-run under timeout so a hung server can never leave the script running.
    # The binary path is already absolute, so the child resolves it the same way.
    RUNAWAY_TEST_CHILD=1 exec timeout --kill-after=5 "$TIMEOUT_SECONDS" "$0" ${BINARY_ARG:+"$BINARY_ARG"}
  fi
  # Without timeout(1) the run is still bounded: every curl has --max-time and
  # every wait loop has a fixed iteration count.
  echo "note: timeout(1) not found, running without the ${TIMEOUT_SECONDS}s hard cap" >&2
fi

WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/oberwatch-runaway.XXXXXX")"
BINARY="${BINARY_ARG:-${WORK_DIR}/oberwatch}"
MOCK_BINARY="${WORK_DIR}/mock-upstream"
CONFIG="${WORK_DIR}/oberwatch.toml"
DB="${WORK_DIR}/oberwatch.db"
COOKIES="${WORK_DIR}/cookies.txt"
SERVER_LOG="${WORK_DIR}/oberwatch.log"
MOCK_LOG="${WORK_DIR}/mock.log"
OW_PID=""
MOCK_PID=""

cleanup() {
  local status=$?
  set +e
  for pid in "$OW_PID" "$MOCK_PID"; do
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null
      for _ in $(seq 1 20); do
        kill -0 "$pid" 2>/dev/null || break
        sleep 0.1
      done
      kill -9 "$pid" 2>/dev/null
      wait "$pid" 2>/dev/null
    fi
  done
  if [ "$status" -ne 0 ] && [ -f "$SERVER_LOG" ]; then
    echo "--- oberwatch log (last 40 lines) ---"
    tail -n 40 "$SERVER_LOG" || true
  fi
  rm -rf "$WORK_DIR"
  exit "$status"
}
trap cleanup EXIT INT TERM

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

find_go() {
  if command -v go >/dev/null 2>&1; then
    command -v go
    return
  fi
  local candidate
  for candidate in /usr/local/go/bin/go "$HOME/.local/go/bin/go" "$HOME/go/bin/go"; do
    if [ -x "$candidate" ]; then
      echo "$candidate"
      return
    fi
  done
  fail "go toolchain not found"
}
GO="$(find_go)"
export GOCACHE="${GOCACHE:-/tmp/go-build}"
export GOFLAGS="${GOFLAGS:-}"

port_in_use() {
  curl -s --max-time 1 -o /dev/null "http://127.0.0.1:$1/" 2>/dev/null
}
for p in "$PORT" "$MOCK_PORT"; do
  if port_in_use "$p"; then
    fail "port $p is already in use; set RUNAWAY_TEST_PORT / RUNAWAY_TEST_MOCK_PORT"
  fi
done

if [ -z "$BINARY_ARG" ]; then
  echo "==> Building oberwatch..."
  "$GO" build -o "$BINARY" ./cmd/oberwatch
else
  [ -x "$BINARY" ] || fail "oberwatch binary $BINARY is not executable"
  echo "==> Using oberwatch binary $BINARY"
fi

echo "==> Building mock upstream + webhook receiver..."
mkdir -p "${WORK_DIR}/mock"
cat > "${WORK_DIR}/mock/main.go" <<'EOF'
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
)

func main() {
	var mu sync.Mutex
	hits := 0
	alerts := map[string]int{}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		hits++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"mock","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	})
	mux.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Type  string `json:"type"`
			Agent string `json:"agent"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		alerts[payload.Agent+"/"+payload.Type]++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/__state", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"hits": hits, "alerts": alerts})
	})
	addr := fmt.Sprintf("127.0.0.1:%s", os.Args[1])
	log.Fatal(http.ListenAndServe(addr, mux))
}
EOF
cat > "${WORK_DIR}/mock/go.mod" <<'EOF'
module mockupstream

go 1.26
EOF
(cd "${WORK_DIR}/mock" && "$GO" build -o "$MOCK_BINARY" .)

cat > "$CONFIG" <<EOF
[server]
port = ${PORT}
host = "127.0.0.1"

[upstream]
default_provider = "openai"

[upstream.openai]
base_url = "${MOCK}"

[alerts]
webhook_url = "${MOCK}/webhook"

[trace]
sqlite_path = "${DB}"
storage = "sqlite"

[gate]
enabled = true

[gate.runaway]
enabled = true
max_requests = ${MAX_REQUESTS}
window_seconds = ${WINDOW_SECONDS}

[[pricing]]
model              = "gpt-4o"
provider           = "openai"
input_per_million  = 2.50
output_per_million = 10.00
EOF

echo "==> Starting mock upstream on :${MOCK_PORT}..."
"$MOCK_BINARY" "$MOCK_PORT" > "$MOCK_LOG" 2>&1 &
MOCK_PID=$!

echo "==> Starting oberwatch on :${PORT}..."
"$BINARY" serve --config "$CONFIG" > "$SERVER_LOG" 2>&1 &
OW_PID=$!

wait_ready() {
  local url="$1" name="$2" i
  for i in $(seq 1 50); do
    if curl -sf --max-time 2 -o /dev/null "$url"; then
      return 0
    fi
    sleep 0.2
  done
  fail "$name did not become ready"
}
wait_ready "${MOCK}/__state" "mock upstream"
wait_ready "${API}/health" "oberwatch"
echo "==> Both servers are ready."

echo "==> Completing admin setup and logging in..."
SETUP_CODE=$(curl -s --max-time 5 -o /dev/null -w "%{http_code}" -X POST "${API}/setup" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"${ADMIN_USER}\",\"password\":\"${ADMIN_PASS}\",\"confirm_password\":\"${ADMIN_PASS}\"}")
[ "$SETUP_CODE" = "200" ] || [ "$SETUP_CODE" = "201" ] || fail "setup returned HTTP $SETUP_CODE"
LOGIN_CODE=$(curl -s --max-time 5 -o /dev/null -w "%{http_code}" -c "$COOKIES" -X POST "${API}/login" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"${ADMIN_USER}\",\"password\":\"${ADMIN_PASS}\"}")
[ "$LOGIN_CODE" = "200" ] || fail "login returned HTTP $LOGIN_CODE"

api_get() {
  curl -sf --max-time 5 -b "$COOKIES" "${API}$1"
}

api_post() {
  curl -s --max-time 5 -o /dev/null -w "%{http_code}" -b "$COOKIES" -X POST "${API}$1"
}

send_request() {
  local agent="$1" out="$2"
  curl -s --max-time 5 -o "$out" -w "%{http_code}" \
    -X POST "${BASE}/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -H "X-Oberwatch-Agent: ${agent}" \
    -H "Authorization: Bearer mock-key" \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}'
}

count_in() {
  # count_in <text> <needle>: number of occurrences of needle in text.
  # grep exits 1 on no match, so keep the pipeline status zero under pipefail.
  local n
  n=$(printf '%s' "$1" | grep -o -- "$2" | wc -l | tr -d ' ' || true)
  echo "${n:-0}"
}

mock_hits() {
  curl -sf --max-time 5 "${MOCK}/__state" | grep -o '"hits":[0-9]*' | cut -d: -f2
}

mock_alert_count() {
  # mock_alert_count <agent> <type>
  local state
  state=$(curl -sf --max-time 5 "${MOCK}/__state")
  local n
  n=$(printf '%s' "$state" | grep -o "\"$1/$2\":[0-9]*" | cut -d: -f2 || true)
  echo "${n:-0}"
}

stored_alert_count() {
  # stored_alert_count <agent> <type>
  local body
  body=$(api_get "/alerts?agent=$1")
  count_in "$body" "\"type\":\"$2\""
}

budget_status() {
  api_get "/budgets/$1" | grep -o '"status":"[a-z_]*"' | head -1 | cut -d'"' -f4
}

expect_code() {
  # expect_code <label> <got> <want>
  if [ "$2" != "$3" ]; then
    fail "$1: HTTP $2, want $3"
  fi
  echo "  $1: HTTP $2"
}

expect_eq() {
  # expect_eq <label> <got> <want>
  if [ "$2" != "$3" ]; then
    fail "$1: got $2, want $3"
  fi
  echo "  $1: $2"
}

RESP="${WORK_DIR}/resp.json"

echo "==> Phase 1: ${MAX_REQUESTS} requests inside the window are allowed"
for i in $(seq 1 "$MAX_REQUESTS"); do
  expect_code "request $i (${AGENT})" "$(send_request "$AGENT" "$RESP")" 200
done
expect_eq "upstream hits" "$(mock_hits)" "$MAX_REQUESTS"
expect_eq "budget status" "$(budget_status "$AGENT")" "active"

echo "==> Phase 2: request $((MAX_REQUESTS + 1)) trips the detector"
expect_code "request $((MAX_REQUESTS + 1)) (${AGENT})" "$(send_request "$AGENT" "$RESP")" 429
grep -q '"code":"agent_killed"' "$RESP" || fail "429 body lacks agent_killed: $(cat "$RESP")"
grep -q "runaway request volume" "$RESP" || fail "429 body lacks runaway message: $(cat "$RESP")"
echo "  error.code=agent_killed with runaway message"
for i in 1 2; do
  expect_code "post-kill request $i (${AGENT})" "$(send_request "$AGENT" "$RESP")" 429
  grep -q '"code":"agent_killed"' "$RESP" || fail "post-kill body lacks agent_killed: $(cat "$RESP")"
done
expect_code "bystander request (${BYSTANDER})" "$(send_request "$BYSTANDER" "$RESP")" 200
expect_eq "upstream hits (killed requests never forwarded)" "$(mock_hits)" "$((MAX_REQUESTS + 1))"
expect_eq "budget status" "$(budget_status "$AGENT")" "killed"

echo "==> Phase 3: exactly one runaway_detected and one agent_killed alert"
# The webhook is delivered synchronously before the 429 is written, but give it
# a brief bounded grace period anyway.
for _ in $(seq 1 20); do
  if [ "$(mock_alert_count "$AGENT" agent_killed)" = "1" ]; then break; fi
  sleep 0.1
done
expect_eq "stored runaway_detected" "$(stored_alert_count "$AGENT" runaway_detected)" 1
expect_eq "stored agent_killed" "$(stored_alert_count "$AGENT" agent_killed)" 1
expect_eq "webhook runaway_detected" "$(mock_alert_count "$AGENT" runaway_detected)" 1
expect_eq "webhook agent_killed" "$(mock_alert_count "$AGENT" agent_killed)" 1
expect_eq "bystander stored alerts" "$(count_in "$(api_get "/alerts?agent=${BYSTANDER}")" '"type":')" 0

echo "==> Phase 4: kill is sticky after the ${WINDOW_SECONDS}s window"
sleep "$((WINDOW_SECONDS + 1))"
expect_code "request after window (${AGENT})" "$(send_request "$AGENT" "$RESP")" 429
grep -q '"code":"agent_killed"' "$RESP" || fail "sticky body lacks agent_killed: $(cat "$RESP")"
expect_eq "budget status" "$(budget_status "$AGENT")" "killed"
expect_eq "stored agent_killed (unchanged)" "$(stored_alert_count "$AGENT" agent_killed)" 1

echo "==> Phase 5: manual enable restores traffic"
expect_code "POST /budgets/${AGENT}/enable" "$(api_post "/budgets/${AGENT}/enable")" 200
expect_eq "budget status" "$(budget_status "$AGENT")" "active"
for i in 1 2; do
  expect_code "post-enable request $i (${AGENT})" "$(send_request "$AGENT" "$RESP")" 200
done
expect_eq "upstream hits" "$(mock_hits)" "$((MAX_REQUESTS + 3))"
expect_eq "stored runaway_detected (unchanged)" "$(stored_alert_count "$AGENT" runaway_detected)" 1
expect_eq "stored agent_killed (unchanged)" "$(stored_alert_count "$AGENT" agent_killed)" 1
expect_eq "webhook agent_killed (unchanged)" "$(mock_alert_count "$AGENT" agent_killed)" 1

echo ""
echo "PASS: runaway detection trigger and recovery verified against mock upstream."
