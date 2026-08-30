#!/usr/bin/env bash
# End-to-end check of per-task budgets against a mock upstream.
#
# Starts a mock OpenAI-compatible upstream that records whether any
# X-Oberwatch-* header reached it, starts oberwatch with a gate-level task cap
# and one per-agent override, then proves:
#   - requests without a task ID, or with a blank one, are not task-budgeted
#     and never form a shared bucket
#   - the cap rejects a projected over-cap request with a structured
#     task_budget_exceeded 429 before it reaches the upstream
#   - X-Oberwatch-Task (and every other X-Oberwatch-* header) is stripped
#     before the request reaches the upstream
#   - task spend and agent spend are tracked separately; tasks are independent
#   - a per-agent task_budget_usd wins over gate.task_budget_usd, and one task
#     shared by several agents is capped by the limit of the sending agent
#   - GET /costs and GET /costs/export filter on the exact task ID
#   - settled task totals survive a restart and are still enforced
#   - POST /tasks/{task_id}/reset clears settled spend and traffic resumes,
#     while agent spend is untouched
#
# Usage: scripts/dev/test-task-budget.sh [path-to-oberwatch-binary]
# Without an argument the script builds ./cmd/oberwatch into a temp dir. The
# mock upstream is always built from source, so a Go toolchain is required.
#
# Everything runs on loopback, needs no provider credentials, is bounded by
# TASK_BUDGET_TEST_TIMEOUT seconds, and cleans up processes and files on exit.
set -euo pipefail

TIMEOUT_SECONDS="${TASK_BUDGET_TEST_TIMEOUT:-120}"
if [ -z "${TASK_BUDGET_TEST_CHILD:-}" ]; then
  if command -v timeout >/dev/null 2>&1; then
    # Re-run under timeout so a hung server can never leave the script running.
    TASK_BUDGET_TEST_CHILD=1 exec timeout --kill-after=5 "$TIMEOUT_SECONDS" "$0" "$@"
  fi
  # Without timeout(1) the run is still bounded: every curl has --max-time and
  # every wait loop has a fixed iteration count.
  echo "note: timeout(1) not found, running without the ${TIMEOUT_SECONDS}s hard cap" >&2
fi

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

PORT="${TASK_BUDGET_TEST_PORT:-18396}"
MOCK_PORT="${TASK_BUDGET_TEST_MOCK_PORT:-18397}"
# Every forwarded request settles to exactly $0.50: the mock bills 250k prompt
# and 250k completion tokens, priced at $1 per million each.
TASK_LIMIT="1.00"
TIGHT_TASK_LIMIT="0.30"
AGENT="task-agent"
TIGHT_AGENT="tight-agent"
FREE_AGENT="free-agent"
ADMIN_USER="task-admin"
ADMIN_PASS="task-password"

BASE="http://127.0.0.1:${PORT}"
API="${BASE}/_oberwatch/api/v1"
MOCK="http://127.0.0.1:${MOCK_PORT}"

WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/oberwatch-task-budget.XXXXXX")"
BINARY="${BINARY_ARG:-${WORK_DIR}/oberwatch}"
MOCK_BINARY="${WORK_DIR}/mock-upstream"
CONFIG="${WORK_DIR}/oberwatch.toml"
DB="${WORK_DIR}/oberwatch.db"
COOKIES="${WORK_DIR}/cookies.txt"
SERVER_LOG="${WORK_DIR}/oberwatch.log"
MOCK_LOG="${WORK_DIR}/mock.log"
RESP="${WORK_DIR}/resp.json"
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
    fail "port $p is already in use; set TASK_BUDGET_TEST_PORT / TASK_BUDGET_TEST_MOCK_PORT"
  fi
done

if [ -z "$BINARY_ARG" ]; then
  echo "==> Building oberwatch..."
  "$GO" build -o "$BINARY" ./cmd/oberwatch
else
  [ -x "$BINARY" ] || fail "oberwatch binary $BINARY is not executable"
  echo "==> Using oberwatch binary $BINARY"
fi

echo "==> Building mock upstream..."
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
	"strings"
	"sync"
)

func main() {
	var mu sync.Mutex
	hits := 0
	taskHeaderSeen := 0
	oberwatchHeaderSeen := 0

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		hits++
		if _, present := r.Header["X-Oberwatch-Task"]; present {
			taskHeaderSeen++
		}
		for key := range r.Header {
			if strings.HasPrefix(strings.ToLower(key), "x-oberwatch-") {
				oberwatchHeaderSeen++
				break
			}
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		// 250k prompt + 250k completion tokens: $0.50 at $1 per million each.
		_, _ = w.Write([]byte(`{"id":"mock","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":250000,"completion_tokens":250000,"total_tokens":500000}}`))
	})
	mux.HandleFunc("/__state", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{
			"hits":                  hits,
			"task_header_seen":      taskHeaderSeen,
			"oberwatch_header_seen": oberwatchHeaderSeen,
		})
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

[trace]
sqlite_path = "${DB}"
storage = "sqlite"

[gate]
enabled = true
task_budget_usd = ${TASK_LIMIT}

[gate.default_budget]
limit_usd        = 100.00
period           = "daily"
action_on_exceed = "alert"

[[pricing]]
model              = "gpt-4o"
provider           = "openai"
input_per_million  = 1.00
output_per_million = 1.00
EOF

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

start_oberwatch() {
  "$BINARY" serve --config "$CONFIG" >> "$SERVER_LOG" 2>&1 &
  OW_PID=$!
  wait_ready "${API}/health" "oberwatch"
}

stop_oberwatch() {
  local status=0
  kill -TERM "$OW_PID"
  # The whole run is bounded by timeout(1), so a shutdown that hangs still fails.
  wait "$OW_PID" || status=$?
  OW_PID=""
  echo "  oberwatch exited with status $status after SIGTERM"
  # Settled task totals are flushed during shutdown, so a crash here would
  # leave the restart checks below proving nothing.
  [ "$status" -eq 0 ] || fail "oberwatch did not shut down cleanly on SIGTERM (status $status)"
}

login() {
  local code
  code=$(curl -s --max-time 5 -o /dev/null -w "%{http_code}" -c "$COOKIES" -X POST "${API}/login" \
    -H "Content-Type: application/json" \
    -d "{\"username\":\"${ADMIN_USER}\",\"password\":\"${ADMIN_PASS}\"}")
  [ "$code" = "200" ] || fail "login returned HTTP $code"
}

echo "==> Starting mock upstream on :${MOCK_PORT}..."
"$MOCK_BINARY" "$MOCK_PORT" > "$MOCK_LOG" 2>&1 &
MOCK_PID=$!
wait_ready "${MOCK}/__state" "mock upstream"

echo "==> Starting oberwatch on :${PORT}..."
start_oberwatch
echo "==> Both servers are ready."

echo "==> Completing admin setup and logging in..."
SETUP_CODE=$(curl -s --max-time 5 -o /dev/null -w "%{http_code}" -X POST "${API}/setup" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"${ADMIN_USER}\",\"password\":\"${ADMIN_PASS}\",\"confirm_password\":\"${ADMIN_PASS}\"}")
[ "$SETUP_CODE" = "200" ] || [ "$SETUP_CODE" = "201" ] || fail "setup returned HTTP $SETUP_CODE"
login

echo "==> Setting the SQLite-backed task-budget override for ${TIGHT_AGENT}..."
TIGHT_BUDGET_CODE=$(curl -s --max-time 5 -o "$RESP" -w "%{http_code}" -b "$COOKIES" \
  -X PUT "${API}/budgets/${TIGHT_AGENT}" \
  -H "Content-Type: application/json" \
  -d "{\"limit_usd\":100.00,\"period\":\"daily\",\"action_on_exceed\":\"alert\",\"task_budget_usd\":${TIGHT_TASK_LIMIT}}")
[ "$TIGHT_BUDGET_CODE" = "200" ] || fail "task-budget override update returned HTTP $TIGHT_BUDGET_CODE: $(cat "$RESP")"

api_get() {
  curl -sf --max-time 5 -b "$COOKIES" "${API}$1"
}

api_post() {
  # api_post <path> <out>: prints the HTTP code, writes the body to <out>.
  curl -s --max-time 5 -o "$2" -w "%{http_code}" -b "$COOKIES" -X POST "${API}$1"
}

send_request() {
  # send_request <agent> <out> [extra curl args...]: prints the HTTP code.
  local agent="$1" out="$2"
  shift 2
  curl -s --max-time 5 -o "$out" -w "%{http_code}" \
    -X POST "${BASE}/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -H "X-Oberwatch-Agent: ${agent}" \
    -H "Authorization: Bearer mock-key" \
    "$@" \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}'
}

json_field() {
  # json_field <key> <file>: first scalar value stored under key, quotes removed.
  grep -o "\"$1\":[^,}]*" "$2" | head -1 | cut -d: -f2- | tr -d '"'
}

mock_state() {
  # mock_state <key>
  curl -sf --max-time 5 "${MOCK}/__state" | grep -o "\"$1\":[0-9]*" | cut -d: -f2
}

task_count() {
  # Number of task buckets reported by GET /tasks.
  api_get "/tasks" > "$RESP"
  grep -o '"task_id"' "$RESP" | wc -l | tr -d ' '
}

agent_spent() {
  # agent_spent <agent>: settled agent spend from GET /budgets/{agent}.
  api_get "/budgets/$1" > "$RESP"
  json_field spent_usd "$RESP"
}

expect_code() {
  # expect_code <label> <got> <want>
  if [ "$2" != "$3" ]; then
    fail "$1: HTTP $2, want $3 (body: $(cat "$RESP" 2>/dev/null))"
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

expect_field() {
  # expect_field <label> <key> <want>: checks a field of the body in $RESP.
  expect_eq "$1" "$(json_field "$2" "$RESP")" "$3"
}

expect_num_field() {
  # expect_num_field <label> <key> <want>: numeric compare, so the JSON value 1
  # matches a limit written as 1.00 and the message check below can keep the
  # two-decimal form the server prints.
  local got
  got="$(json_field "$2" "$RESP")"
  if [ -z "$got" ] || ! awk -v a="$got" -v b="$3" 'BEGIN { exit (a + 0 == b + 0) ? 0 : 1 }'; then
    fail "$1: got ${got:-<missing>}, want $3"
  fi
  echo "  $1: $got"
}

expect_task_rejection() {
  # expect_task_rejection <label> <task> <agent> <limit>: $RESP holds a 429 body.
  expect_field "$1: error.code" code task_budget_exceeded
  expect_field "$1: error.task_id" task_id "$2"
  expect_field "$1: error.agent" agent "$3"
  expect_num_field "$1: error.task_budget_limit_usd" task_budget_limit_usd "$4"
  grep -q "would exceed its budget of \$$4" "$RESP" \
    || fail "$1: 429 body lacks the budget message: $(cat "$RESP")"
}

TASK_HEADER="X-Oberwatch-Task"
EXPECTED_HITS=0

echo "==> Phase 1: requests without a task ID are not task-budgeted"
expect_code "request without ${TASK_HEADER} (${FREE_AGENT})" "$(send_request "$FREE_AGENT" "$RESP")" 200
# "Header;" is curl's syntax for sending the header with an empty value.
expect_code "request with empty ${TASK_HEADER} (${FREE_AGENT})" "$(send_request "$FREE_AGENT" "$RESP" -H "${TASK_HEADER};")" 200
EXPECTED_HITS=$((EXPECTED_HITS + 2))
expect_eq "upstream hits" "$(mock_state hits)" "$EXPECTED_HITS"
expect_eq "task buckets" "$(task_count)" 0

echo "==> Phase 2: the task cap rejects before upstream with a structured 429"
for i in 1 2; do
  expect_code "request $i (${AGENT}, task job-a)" "$(send_request "$AGENT" "$RESP" -H "${TASK_HEADER}: job-a")" 200
done
EXPECTED_HITS=$((EXPECTED_HITS + 2))
expect_eq "upstream hits" "$(mock_state hits)" "$EXPECTED_HITS"
api_get "/tasks/job-a" > "$RESP"
expect_field "job-a spent_usd" spent_usd 1
expect_field "job-a limit_usd" limit_usd 1
expect_field "job-a remaining_usd" remaining_usd 0
expect_field "job-a reserved_usd" reserved_usd 0
expect_field "job-a in_flight" in_flight 0
expect_field "job-a request_count" request_count 2
expect_field "job-a status" status exceeded
expect_field "job-a last_agent" last_agent "$AGENT"
expect_code "request 3 (${AGENT}, task job-a)" "$(send_request "$AGENT" "$RESP" -H "${TASK_HEADER}: job-a")" 429
expect_task_rejection "request 3" job-a "$AGENT" 1.00
expect_field "request 3: error.task_budget_spent_usd" task_budget_spent_usd 1
expect_eq "upstream hits (rejected request never forwarded)" "$(mock_state hits)" "$EXPECTED_HITS"
expect_eq "upstream requests carrying ${TASK_HEADER}" "$(mock_state task_header_seen)" 0
expect_eq "upstream requests carrying any X-Oberwatch-* header" "$(mock_state oberwatch_header_seen)" 0

echo "==> Phase 3: tasks are independent and agent spend is tracked separately"
expect_code "request (${AGENT}, task job-b)" "$(send_request "$AGENT" "$RESP" -H "${TASK_HEADER}: job-b")" 200
EXPECTED_HITS=$((EXPECTED_HITS + 1))
expect_eq "upstream hits" "$(mock_state hits)" "$EXPECTED_HITS"
api_get "/tasks/job-b" > "$RESP"
expect_field "job-b spent_usd" spent_usd 0.5
expect_field "job-b status" status active
expect_eq "task buckets" "$(task_count)" 2
expect_eq "${AGENT} agent spend (3 requests)" "$(agent_spent "$AGENT")" 1.5

echo "==> Phase 4: per-agent task cap wins and a shared task is capped per sender"
expect_code "request 1 (${TIGHT_AGENT}, task job-c)" "$(send_request "$TIGHT_AGENT" "$RESP" -H "${TASK_HEADER}: job-c")" 200
EXPECTED_HITS=$((EXPECTED_HITS + 1))
expect_code "request 2 (${TIGHT_AGENT}, task job-c)" "$(send_request "$TIGHT_AGENT" "$RESP" -H "${TASK_HEADER}: job-c")" 429
expect_task_rejection "tight request 2" job-c "$TIGHT_AGENT" 0.30
expect_code "request (${AGENT}, task job-c under the gate cap)" "$(send_request "$AGENT" "$RESP" -H "${TASK_HEADER}: job-c")" 200
EXPECTED_HITS=$((EXPECTED_HITS + 1))
api_get "/tasks/job-c" > "$RESP"
expect_field "job-c spent_usd (shared total)" spent_usd 1
expect_field "job-c last_agent" last_agent "$AGENT"
expect_field "job-c limit_usd (cap of the last sender)" limit_usd 1
expect_field "job-c status" status exceeded
expect_code "request (${TIGHT_AGENT}, task job-c)" "$(send_request "$TIGHT_AGENT" "$RESP" -H "${TASK_HEADER}: job-c")" 429
expect_task_rejection "tight request 3" job-c "$TIGHT_AGENT" 0.30
expect_code "request (${AGENT}, task job-c)" "$(send_request "$AGENT" "$RESP" -H "${TASK_HEADER}: job-c")" 429
expect_task_rejection "gate request" job-c "$AGENT" 1.00
expect_eq "upstream hits" "$(mock_state hits)" "$EXPECTED_HITS"

echo "==> Phase 5: cost queries filter on the exact task ID"
# Cost rows are written asynchronously; wait for the two job-a rows.
COSTS=""
for _ in $(seq 1 50); do
  COSTS=$(api_get "/costs?task=job-a&group_by=none")
  if printf '%s' "$COSTS" | grep -q '"total_requests":2[,}]'; then break; fi
  sleep 0.1
done
printf '%s' "$COSTS" > "$RESP"
expect_field "GET /costs?task=job-a total_requests" total_requests 2
expect_field "GET /costs?task=job-a total_usd" total_usd 1
api_get "/costs?task=job-&group_by=none" > "$RESP"
expect_field "GET /costs?task=job- total_requests (prefix does not match)" total_requests 0
api_get "/costs/export?task=job-a&group_by=agent" > "$RESP"
expect_eq "CSV header" "$(head -n 1 "$RESP" | tr -d '\r')" "agent,model,provider,requests,input_tokens,output_tokens,cost_usd"
expect_eq "CSV data rows" "$(tail -n +2 "$RESP" | grep -c .)" 1
grep -q "^${AGENT},.*,1.00000000$" "$RESP" || fail "CSV row lacks ${AGENT} with 1.00000000: $(cat "$RESP")"
echo "  CSV row: $(tail -n +2 "$RESP")"

echo "==> Phase 6: settled task totals survive a restart"
stop_oberwatch
start_oberwatch
login
expect_eq "task buckets after restart" "$(task_count)" 3
api_get "/tasks/job-a" > "$RESP"
expect_field "job-a spent_usd after restart" spent_usd 1
expect_field "job-a request_count after restart" request_count 2
expect_field "job-a status after restart" status exceeded
expect_code "request after restart (${AGENT}, task job-a)" "$(send_request "$AGENT" "$RESP" -H "${TASK_HEADER}: job-a")" 429
expect_task_rejection "post-restart request" job-a "$AGENT" 1.00
expect_eq "upstream hits (still rejected before upstream)" "$(mock_state hits)" "$EXPECTED_HITS"

echo "==> Phase 7: reset clears settled task spend and traffic resumes"
AGENT_SPENT_BEFORE="$(agent_spent "$AGENT")"
expect_code "POST /tasks/job-a/reset" "$(api_post "/tasks/job-a/reset" "$RESP")" 200
expect_field "reset response spent_usd" spent_usd 0
expect_field "reset response request_count" request_count 0
expect_field "reset response status" status active
expect_eq "${AGENT} agent spend unchanged by task reset" "$(agent_spent "$AGENT")" "$AGENT_SPENT_BEFORE"
expect_code "request after reset (${AGENT}, task job-a)" "$(send_request "$AGENT" "$RESP" -H "${TASK_HEADER}: job-a")" 200
EXPECTED_HITS=$((EXPECTED_HITS + 1))
expect_eq "upstream hits" "$(mock_state hits)" "$EXPECTED_HITS"
api_get "/tasks/job-a" > "$RESP"
expect_field "job-a spent_usd after reset and one request" spent_usd 0.5
expect_field "job-a request_count after reset and one request" request_count 1
expect_field "job-a status" status active
expect_eq "${AGENT} agent spend after one more request" "$(agent_spent "$AGENT")" \
  "$(awk -v before="$AGENT_SPENT_BEFORE" 'BEGIN { printf "%g", before + 0.5 }')"
expect_code "POST /tasks/does-not-exist/reset" "$(api_post "/tasks/does-not-exist/reset" "$RESP")" 404
expect_field "unknown task reset error.code" code not_found

echo ""
echo "PASS: per-task budget enforcement, persistence and reset verified against mock upstream."
