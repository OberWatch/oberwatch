#!/bin/sh
set -eu

IMAGE=${OBERWATCH_TEST_IMAGE:-oberwatch-docker-test:local-$$}
CONTAINER=${OBERWATCH_TEST_CONTAINER:-oberwatch-docker-test-$$}
VOLUME=${OBERWATCH_TEST_VOLUME:-oberwatch-docker-test-data-$$}
TEMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/oberwatch-docker-health.XXXXXX")
RESPONSE_FILE=$TEMP_DIR/response
HEALTH_URL=

cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  docker image rm -f "$IMAGE" >/dev/null 2>&1 || true
  docker volume rm -f "$VOLUME" >/dev/null 2>&1 || true
  rm -rf "$TEMP_DIR"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  docker logs "$CONTAINER" >&2 2>/dev/null || true
  exit 1
}

command -v docker >/dev/null 2>&1 || fail 'docker is required'
command -v curl >/dev/null 2>&1 || fail 'curl is required'

docker build --tag "$IMAGE" .
docker volume create "$VOLUME" >/dev/null
docker run --detach \
  --name "$CONTAINER" \
  --publish 127.0.0.1::8080 \
  --volume "$VOLUME":/data \
  "$IMAGE" >/dev/null

[ "$(docker inspect --format '{{.State.Running}}' "$CONTAINER")" = true ] || \
  fail 'container is not running'
printf 'PASS: container is running\n'

PORT_MAPPING=$(docker port "$CONTAINER" 8080/tcp)
HOST_PORT=${PORT_MAPPING##*:}
[ -n "$HOST_PORT" ] || fail 'published port was not assigned'
HEALTH_URL=http://127.0.0.1:${HOST_PORT}/_oberwatch/api/v1/health

http_attempt=1
http_ready=false
while [ "$http_attempt" -le 30 ]; do
  HTTP_STATUS=$(curl --silent --show-error --max-time 2 \
    --output "$RESPONSE_FILE" --write-out '%{http_code}' "$HEALTH_URL" 2>/dev/null || true)
  RESPONSE=$(cat "$RESPONSE_FILE" 2>/dev/null || true)
  if [ "$HTTP_STATUS" = 200 ]; then
    case "$RESPONSE" in
      *'"status":"ok"'*)
        http_ready=true
        break
        ;;
    esac
  fi
  http_attempt=$((http_attempt + 1))
  [ "$http_attempt" -le 30 ] && sleep 1
done
[ "$http_ready" = true ] || fail 'health endpoint did not return HTTP 200 with status ok'
printf 'PASS: unauthenticated health endpoint returned HTTP 200 with status ok\n'

docker exec "$CONTAINER" test -f /data/oberwatch.db || \
  fail 'default SQLite database was not created in /data'
printf 'PASS: default SQLite database exists in the named volume\n'

health_attempt=1
docker_healthy=false
while [ "$health_attempt" -le 30 ]; do
  HEALTH_STATUS=$(docker inspect --format '{{.State.Health.Status}}' "$CONTAINER")
  if [ "$HEALTH_STATUS" = healthy ]; then
    docker_healthy=true
    break
  fi
  [ "$HEALTH_STATUS" != unhealthy ] || fail 'Docker marked the container unhealthy'
  health_attempt=$((health_attempt + 1))
  [ "$health_attempt" -le 30 ] && sleep 1
done
[ "$docker_healthy" = true ] || fail 'Docker health status did not become healthy'
printf 'PASS: Docker marked the container healthy\n'
