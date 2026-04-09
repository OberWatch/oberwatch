#!/usr/bin/env bash
set -euo pipefail

export PATH=/usr/local/go/bin:$PATH
export GOCACHE=/tmp/go-build

echo "==> Testing model auto-downgrade chain"

TEMP_DIR=$(mktemp -d)
trap 'rm -rf "$TEMP_DIR"' EXIT

CONFIG_FILE="$TEMP_DIR/oberwatch.toml"
DB_FILE="$TEMP_DIR/oberwatch.db"
BINARY="$TEMP_DIR/oberwatch"

# Build binary first
echo "==> Building oberwatch binary..."
go build -o "$BINARY" ./cmd/oberwatch

# Strategy: set pricing at $1,000,000/M so even 1 estimated token = $1.00.
# Budget limit = $10. Threshold = 50% = $5.
# First real request body is ~50 bytes ≈ 12 tokens estimated → cost estimate ~$12 > 50% threshold.
# This means the FIRST request will trigger downgrade.
cat > "$CONFIG_FILE" <<EOF
[server]
port = 18080
admin_token = "test-token"

[upstream.openai]
base_url = "http://localhost:18081"

[upstream.anthropic]
base_url = "http://localhost:18082"

[trace]
sqlite_path = "$DB_FILE"

[gate]
enabled = true
downgrade_threshold_pct = 50.0
default_downgrade_chain = []

[gate.default_budget]
limit_usd = 10.0
period = "hourly"
action_on_exceed = "reject"

# Test 1: explicit downgrade chain overrides builtin
[[gate.agents]]
name = "test-agent-explicit"
limit_usd = 10.0
period = "hourly"
action_on_exceed = "downgrade"
downgrade_chain = ["gpt-4o", "gpt-4o-mini"]

# Test 2: no chain — uses builtin OpenAI chain (gpt-4o → gpt-4.1 → …)
[[gate.agents]]
name = "test-agent-builtin-openai"
limit_usd = 10.0
period = "hourly"
action_on_exceed = "downgrade"

# Test 3: no chain — uses builtin Anthropic chain (claude-opus-4-6 → claude-sonnet-4-6 → …)
[[gate.agents]]
name = "test-agent-builtin-anthropic"
limit_usd = 10.0
period = "hourly"
action_on_exceed = "downgrade"

[[pricing]]
model = "gpt-4o"
input_per_million = 1000000.0
output_per_million = 1000000.0

[[pricing]]
model = "gpt-4o-mini"
input_per_million = 100000.0
output_per_million = 100000.0

[[pricing]]
model = "claude-opus-4-6"
input_per_million = 1000000.0
output_per_million = 1000000.0
EOF

# Start mock OpenAI upstream that echoes back the model from the request body
python3 - <<'PYEOF' &
import http.server, socketserver, json

class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length) if length else b"{}"
        try:
            model = json.loads(body).get("model", "gpt-4o")
        except Exception:
            model = "gpt-4o"
        response = {
            "id": "test-openai",
            "object": "chat.completion",
            "created": 1234567890,
            "model": model,
            "choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}],
            "usage": {"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150}
        }
        payload = json.dumps(response).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)
    def log_message(self, *args): pass

socketserver.TCPServer.allow_reuse_address = True
with socketserver.TCPServer(("", 18081), Handler) as s:
    s.serve_forever()
PYEOF
MOCK_OPENAI_PID=$!

# Start mock Anthropic upstream that echoes back the model
python3 - <<'PYEOF' &
import http.server, socketserver, json

class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length) if length else b"{}"
        try:
            model = json.loads(body).get("model", "claude-opus-4-6")
        except Exception:
            model = "claude-opus-4-6"
        response = {
            "id": "test-anthropic",
            "type": "message",
            "role": "assistant",
            "model": model,
            "content": [{"type": "text", "text": "ok"}],
            "stop_reason": "end_turn",
            "usage": {"input_tokens": 100, "output_tokens": 50}
        }
        payload = json.dumps(response).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)
    def log_message(self, *args): pass

socketserver.TCPServer.allow_reuse_address = True
with socketserver.TCPServer(("", 18082), Handler) as s:
    s.serve_forever()
PYEOF
MOCK_ANTHROPIC_PID=$!

trap 'kill $MOCK_OPENAI_PID $MOCK_ANTHROPIC_PID 2>/dev/null || true; rm -rf "$TEMP_DIR"' EXIT

# Wait for mock servers
sleep 1

# Start oberwatch
"$BINARY" serve --config "$CONFIG_FILE" &
OBERWATCH_PID=$!
trap 'kill $OBERWATCH_PID $MOCK_OPENAI_PID $MOCK_ANTHROPIC_PID 2>/dev/null || true; rm -rf "$TEMP_DIR"' EXIT

# Wait for oberwatch to start
sleep 2

# Verify oberwatch is up
if ! curl -sf http://localhost:18080/_oberwatch/api/v1/health > /dev/null 2>&1; then
  echo "FAIL: Oberwatch did not start"
  exit 1
fi
echo "✓ Oberwatch is running"

# ─── Test 1: Explicit downgrade chain (gpt-4o → gpt-4o-mini) ─────────────────
echo ""
echo "--- Test 1: Explicit chain: gpt-4o → gpt-4o-mini ---"
RESP1=$(curl -s -i -X POST http://localhost:18080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "X-Oberwatch-Agent: test-agent-explicit" \
  -H "Authorization: Bearer sk-test" \
  -d '{"model": "gpt-4o", "messages": [{"role": "user", "content": "hello"}]}')

if ! echo "$RESP1" | grep -qi "X-Oberwatch-Downgraded: true"; then
  echo "FAIL: Expected X-Oberwatch-Downgraded: true"
  echo "$RESP1"
  exit 1
fi
if ! echo "$RESP1" | grep -qi "X-Oberwatch-Original-Model: gpt-4o"; then
  echo "FAIL: Expected X-Oberwatch-Original-Model: gpt-4o"
  echo "$RESP1"
  exit 1
fi
# Explicit chain → upstream received gpt-4o-mini (not gpt-4.1 from builtin)
if ! echo "$RESP1" | grep -qE '"model":\s*"gpt-4o-mini"'; then
  echo "FAIL: Expected upstream to receive gpt-4o-mini (explicit chain)"
  echo "$RESP1"
  exit 1
fi
echo "✓ Explicit chain: gpt-4o downgraded to gpt-4o-mini (not builtin gpt-4.1)"

# ─── Test 2: Builtin OpenAI chain (gpt-4o → gpt-4.1) ────────────────────────
echo ""
echo "--- Test 2: Builtin OpenAI chain: gpt-4o → gpt-4.1 ---"
RESP2=$(curl -s -i -X POST http://localhost:18080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "X-Oberwatch-Agent: test-agent-builtin-openai" \
  -H "Authorization: Bearer sk-test" \
  -d '{"model": "gpt-4o", "messages": [{"role": "user", "content": "hello"}]}')

if ! echo "$RESP2" | grep -qi "X-Oberwatch-Downgraded: true"; then
  echo "FAIL: Expected X-Oberwatch-Downgraded: true for builtin OpenAI chain"
  echo "$RESP2"
  exit 1
fi
if ! echo "$RESP2" | grep -qi "X-Oberwatch-Original-Model: gpt-4o"; then
  echo "FAIL: Expected X-Oberwatch-Original-Model: gpt-4o"
  echo "$RESP2"
  exit 1
fi
# Builtin OpenAI chain: gpt-4o → gpt-4.1 (first step)
if ! echo "$RESP2" | grep -qE '"model":\s*"gpt-4\.1"'; then
  echo "FAIL: Expected upstream to receive gpt-4.1 (builtin OpenAI chain)"
  echo "$RESP2"
  exit 1
fi
echo "✓ Builtin OpenAI chain: gpt-4o auto-downgraded to gpt-4.1"

# ─── Test 3: Builtin Anthropic chain (claude-opus-4-6 → claude-sonnet-4-6) ───
echo ""
echo "--- Test 3: Builtin Anthropic chain: claude-opus-4-6 → claude-sonnet-4-6 ---"
RESP3=$(curl -s -i -X POST http://localhost:18080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "X-Oberwatch-Agent: test-agent-builtin-anthropic" \
  -H "Authorization: Bearer sk-test" \
  -d '{"model": "claude-opus-4-6", "messages": [{"role": "user", "content": "hello"}]}')

if ! echo "$RESP3" | grep -qi "X-Oberwatch-Downgraded: true"; then
  echo "FAIL: Expected X-Oberwatch-Downgraded: true for builtin Anthropic chain"
  echo "$RESP3"
  exit 1
fi
if ! echo "$RESP3" | grep -qi "X-Oberwatch-Original-Model: claude-opus-4-6"; then
  echo "FAIL: Expected X-Oberwatch-Original-Model: claude-opus-4-6"
  echo "$RESP3"
  exit 1
fi
echo "✓ Builtin Anthropic chain: claude-opus-4-6 auto-downgraded to claude-sonnet-4-6"

# ─── Test 4: No downgrade when model is at end of chain ───────────────────────
# Uses test-agent-builtin-openai (builtin OpenAI chain), model gpt-4.1-mini is
# the last entry in the builtin chain → RewriteModelForDowngrade finds no next model.
# decision.Over = false (budget not fully exceeded yet, just threshold hit) so request
# continues without downgrade header.
echo ""
echo "--- Test 4: Last model in chain — no downgrade ---"
RESP4=$(curl -s -i -X POST http://localhost:18080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "X-Oberwatch-Agent: test-agent-builtin-openai" \
  -H "Authorization: Bearer sk-test" \
  -d '{"model": "gpt-4.1-mini", "messages": [{"role": "user", "content": "hello"}]}')

if echo "$RESP4" | grep -qi "X-Oberwatch-Downgraded: true"; then
  echo "FAIL: Expected no X-Oberwatch-Downgraded header for last model in chain"
  echo "$RESP4"
  exit 1
fi
echo "✓ Last model in chain: no downgrade (gpt-4.1-mini has no successor)"

echo ""
echo "==> All downgrade tests passed!"
