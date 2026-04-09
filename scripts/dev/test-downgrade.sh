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

# Strategy: set gpt-4o pricing at $1,000,000/M so even 1 estimated token = $1.00.
# Budget limit = $10. Threshold = 50% = $5.
# First request body is ~50 bytes ≈ 12 tokens estimated → cost estimate ~$12 > 50% threshold.
# This means the FIRST real request will trigger downgrade immediately.
# We verify the downgrade headers are present.
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

[gate.default_budget]
limit_usd = 10.0
period = "hourly"
action_on_exceed = "reject"

[[gate.agents]]
name = "test-agent"
limit_usd = 10.0
period = "hourly"
action_on_exceed = "downgrade"
downgrade_chain = ["gpt-4o", "gpt-4o-mini"]

[[pricing]]
model = "gpt-4o"
input_per_million = 1000000.0
output_per_million = 1000000.0

[[pricing]]
model = "gpt-4o-mini"
input_per_million = 100000.0
output_per_million = 100000.0
EOF

# Start mock upstream that returns usage
python3 -c '
import http.server
import socketserver
import json

class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        content_length = int(self.headers["Content-Length"])
        body = self.rfile.read(content_length)
        
        # Return response with usage
        response = {
            "id": "test-123",
            "object": "chat.completion",
            "created": 1234567890,
            "model": "gpt-4o",
            "choices": [{
                "index": 0,
                "message": {"role": "assistant", "content": "Hello"},
                "finish_reason": "stop"
            }],
            "usage": {
                "prompt_tokens": 100,
                "completion_tokens": 50,
                "total_tokens": 150
            }
        }
        
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps(response).encode())
    
    def log_message(self, format, *args):
        pass

with socketserver.TCPServer(("", 18081), Handler) as httpd:
    httpd.serve_forever()
' &
MOCK_PID=$!
trap 'kill $MOCK_PID 2>/dev/null || true; rm -rf "$TEMP_DIR"' EXIT

# Wait for mock server
sleep 1

# Start oberwatch
"$BINARY" serve --config "$CONFIG_FILE" &
OBERWATCH_PID=$!
trap 'kill $OBERWATCH_PID 2>/dev/null || true; kill $MOCK_PID 2>/dev/null || true; rm -rf "$TEMP_DIR"' EXIT

# Wait for oberwatch to start
sleep 2

# Verify oberwatch is up
if ! curl -sf http://localhost:18080/_oberwatch/api/v1/health > /dev/null 2>&1; then
  echo "FAIL: Oberwatch did not start"
  exit 1
fi
echo "✓ Oberwatch is running"

# Send request — pricing is $1M/M so even body byte estimate crosses 50% of $10 threshold.
# Gate checks estimated cost before forwarding → ActionDowngrade → rewrite model.
RESPONSE2=$(curl -s -i -X POST http://localhost:18080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "X-Oberwatch-Agent: test-agent" \
  -H "Authorization: Bearer sk-test" \
  -d '{"model": "gpt-4o", "messages": [{"role": "user", "content": "hello"}]}')

echo "--- Response headers ---"
echo "$RESPONSE2" | head -20
echo "---"

# Check for downgrade headers
if ! echo "$RESPONSE2" | grep -qi "X-Oberwatch-Downgraded: true"; then
  echo "FAIL: Second request should have X-Oberwatch-Downgraded: true header"
  echo "--- Response ---"
  echo "$RESPONSE2"
  exit 1
fi

if ! echo "$RESPONSE2" | grep -qi "X-Oberwatch-Original-Model: gpt-4o"; then
  echo "FAIL: Second request should have X-Oberwatch-Original-Model: gpt-4o header"
  echo "--- Response ---"
  echo "$RESPONSE2"
  exit 1
fi

echo "✓ Second request triggered downgrade with correct headers"
echo "==> All downgrade tests passed!"
