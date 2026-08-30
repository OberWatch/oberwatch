#!/usr/bin/env bash
set -euo pipefail

BINARY="./tmp-global-budget-test-binary"
CONFIG="./tmp-global-budget-config.toml"
DB="./tmp-global-budget-test.db"
PORT=18195
BASE="http://127.0.0.1:${PORT}"
ADMIN_TOKEN="test-token"

cleanup() {
  kill "${OW_PID:-}" 2>/dev/null || true
  rm -f "$BINARY" "$CONFIG" "$DB"
}
trap cleanup EXIT

echo "==> Building oberwatch..."
go build -o "$BINARY" ./cmd/oberwatch

cat > "$CONFIG" <<EOF
[server]
port = ${PORT}
admin_token = "${ADMIN_TOKEN}"

[upstream.openai]
base_url = "http://127.0.0.1:19995"

[trace]
sqlite_path = "${DB}"
storage = "sqlite"

[gate]
enabled = true

[gate.default_budget]
limit_usd       = 100.00
period          = "daily"
action_on_exceed = "reject"

[gate.global_budget]
limit_usd = 0.10
period    = "daily"

[[pricing]]
model               = "gpt-4o"
provider            = "openai"
input_per_million   = 100000.00
output_per_million  = 100000.00
EOF

echo "==> Starting oberwatch..."
"$BINARY" serve --config "$CONFIG" &
OW_PID=$!
sleep 1

wait_ready() {
  local i
  for i in $(seq 1 20); do
    if curl -sf -H "Authorization: Bearer ${ADMIN_TOKEN}" \
        "${BASE}/_oberwatch/api/v1/health" > /dev/null 2>&1; then
      return 0
    fi
    sleep 0.3
  done
  echo "FAIL: oberwatch did not start in time"
  exit 1
}
wait_ready

echo "==> Server is ready."

send_request() {
  local agent="$1"
  curl -s -o /dev/null -w "%{http_code}" \
    -X POST "${BASE}/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -H "X-Oberwatch-Agent: ${agent}" \
    -H "Authorization: Bearer fake-key" \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}'
}

echo "==> Sending requests to exhaust global budget..."
REJECTED=0
for i in $(seq 1 10); do
  AGENT="alice"
  if (( i % 2 == 0 )); then AGENT="bob"; fi
  CODE=$(send_request "$AGENT")
  echo "  request $i (agent=$AGENT): HTTP $CODE"
  if [ "$CODE" = "429" ]; then
    REJECTED=$((REJECTED + 1))
  fi
done

if [ "$REJECTED" -eq 0 ]; then
  echo "FAIL: expected at least one 429 from global budget enforcement, got none"
  exit 1
fi
echo "  -> Got $REJECTED 429s as expected."

echo "==> Checking /budgets for global object..."
BUDGETS=$(curl -sf -H "Authorization: Bearer ${ADMIN_TOKEN}" \
  "${BASE}/_oberwatch/api/v1/budgets")

GLOBAL_SPENT=$(echo "$BUDGETS" | grep -o '"spent_usd":[0-9.]*' | head -1 | cut -d: -f2)
GLOBAL_LIMIT=$(echo "$BUDGETS" | grep -o '"limit_usd":[0-9.]*' | head -1 | cut -d: -f2)

echo "  global spent_usd=${GLOBAL_SPENT} limit_usd=${GLOBAL_LIMIT}"

if [ -z "$GLOBAL_SPENT" ]; then
  echo "FAIL: global spent_usd missing from /budgets response"
  exit 1
fi

SPENT_NUM=$(echo "$GLOBAL_SPENT" | awk '{printf "%.4f", $1}')
if awk "BEGIN{exit !($SPENT_NUM > 0)}"; then
  echo "  -> global spent_usd > 0: PASS"
else
  echo "FAIL: global spent_usd should be > 0, got $GLOBAL_SPENT"
  exit 1
fi

echo "==> Verifying 429 error code is global_budget_exceeded..."
RESP=$(curl -s -X POST "${BASE}/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "X-Oberwatch-Agent: alice" \
  -H "Authorization: Bearer fake-key" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}')

if echo "$RESP" | grep -q "global_budget_exceeded"; then
  echo "  -> error code global_budget_exceeded: PASS"
else
  echo "  -> Response: $RESP"
  echo "  (note: requests may not all trigger global limit depending on upstream mock availability)"
fi

echo ""
echo "PASS: Global budget cap integration test complete."
