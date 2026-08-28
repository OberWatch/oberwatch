#!/bin/sh
set -u

failures=0

require_literal() {
  description=$1
  file=$2
  literal=$3

  if [ ! -f "$file" ] || ! grep -F -- "$literal" "$file" >/dev/null 2>&1; then
    printf 'FAIL: %s\n' "$description" >&2
    failures=$((failures + 1))
  else
    printf 'PASS: %s\n' "$description"
  fi
}

require_executable() {
  description=$1
  file=$2

  if [ ! -x "$file" ]; then
    printf 'FAIL: %s\n' "$description" >&2
    failures=$((failures + 1))
  else
    printf 'PASS: %s\n' "$description"
  fi
}

require_literal 'image keeps the oberwatch exec entrypoint' Dockerfile 'ENTRYPOINT ["oberwatch"]'
require_literal 'image keeps serve as the default command' Dockerfile 'CMD ["serve"]'
require_literal 'image runs as the non-root oberwatch user' Dockerfile 'USER oberwatch'
require_literal 'image starts in persistent data directory' Dockerfile 'WORKDIR /data'
require_literal 'persistent data directory is owned by oberwatch' Dockerfile 'chown oberwatch:oberwatch /data'
require_literal 'image declares the data volume' Dockerfile 'VOLUME ["/data"]'
require_literal 'image has a bounded health check' Dockerfile 'HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=5'
require_literal 'image health check uses its unauthenticated endpoint' Dockerfile 'http://127.0.0.1:8080/_oberwatch/api/v1/health'

require_executable 'Docker run integration test exists and is executable' scripts/test-docker-run.sh
require_literal 'Docker run test builds the local image' scripts/test-docker-run.sh 'docker build'
require_literal 'Docker run test creates a named volume' scripts/test-docker-run.sh 'docker volume create'
require_literal 'Docker run test verifies running state' scripts/test-docker-run.sh "'{{.State.Running}}'"
require_literal 'Docker run test polls the unauthenticated health endpoint' scripts/test-docker-run.sh '/_oberwatch/api/v1/health'
require_literal 'Docker run test bounds each HTTP request' scripts/test-docker-run.sh '--max-time'
require_literal 'Docker run test checks the ok response' scripts/test-docker-run.sh '"status":"ok"'
require_literal 'Docker run test verifies Docker healthy state' scripts/test-docker-run.sh "'{{.State.Health.Status}}'"
require_literal 'Docker run test verifies the container runs as non-root' scripts/test-docker-run.sh 'docker exec "$CONTAINER" id -u'
require_literal 'Docker run test verifies no SQLite shared library is needed' scripts/test-docker-run.sh 'ls /usr/lib/libsqlite3.so*'
require_literal 'image builds the binary without cgo' Dockerfile 'CGO_ENABLED=0 go build'
require_literal 'Docker run test creates a private temporary directory' scripts/test-docker-run.sh 'mktemp -d "${TMPDIR:-/tmp}/oberwatch-docker-health.XXXXXX"'
require_literal 'Docker run test keeps curl output in its private temporary directory' scripts/test-docker-run.sh 'RESPONSE_FILE=$TEMP_DIR/response'
require_literal 'Docker run test safely removes its private temporary directory' scripts/test-docker-run.sh 'rm -rf "$TEMP_DIR"'
require_literal 'Docker run test always installs cleanup' scripts/test-docker-run.sh 'trap cleanup EXIT'
require_literal 'pull-request CI runs the Docker integration test' .github/workflows/ci.yml './scripts/test-docker-run.sh'

require_literal 'README Docker example names the container and volume' README.md 'docker run -d --name oberwatch -p 8080:8080 -v oberwatch-data:/data'
require_literal 'README explains config-free persistent startup' README.md 'starts without a config file and stores `./oberwatch.db` in the named `/data` volume'
require_literal 'README explains session-based first run needs no token' README.md 'No admin token is required; first-run setup creates the admin account and starts a cookie-backed session.'
require_literal 'production Docker example names the container and volume' docs/production.md 'docker run -d --name oberwatch -p 127.0.0.1:8080:8080 -v oberwatch-data:/data'
require_literal 'production guide explains session-based first run needs no token' docs/production.md 'No admin token is required; open the dashboard to create the admin account and start a cookie-backed session.'

if [ "$failures" -ne 0 ]; then
  printf '\n%d Docker contract check(s) failed.\n' "$failures" >&2
  exit 1
fi

printf '\nDocker contracts pass.\n'
