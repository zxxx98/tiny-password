#!/usr/bin/env bash
# T02+: isolated end-to-end run. Starts the test compose with an empty data
# directory, asserts baseline HTTP behavior, and always cleans up.
set -eu -o pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# Use plain docker when permitted; fall back to sudo (local dev environments).
if docker info >/dev/null 2>&1; then
  DOCKER="docker"
elif sudo -n docker info >/dev/null 2>&1; then
  DOCKER="sudo docker"
else
  echo "e2e: docker is not accessible" >&2
  exit 1
fi

IMAGE="${IMAGE:-tiny-password:dev}"
export TP_TEST_DATA_DIR="$(mktemp -d /tmp/tiny-password-e2e.XXXXXX)"
# The container runs as UID 10001; a throwaway test dir may be world-writable.
chmod 0777 "$TP_TEST_DATA_DIR"
# Synthetic master key lives outside the data dir (test only, never committed).
KEY_DIR="$(mktemp -d /tmp/tiny-password-key.XXXXXX)"
REQUEST_DIR="$(mktemp -d /tmp/tiny-password-requests.XXXXXX)"
KEY_FILE="$KEY_DIR/master_key"
head -c 32 /dev/urandom > "$KEY_FILE"
chmod 0644 "$KEY_FILE"  # container reads as UID 10001; synthetic key only
CONTAINER_NAME="tiny-password-e2e"
cleanup() {
  $DOCKER rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
  rm -rf "$TP_TEST_DATA_DIR" "$KEY_DIR" "$REQUEST_DIR"
}
trap cleanup EXIT

echo "e2e: starting test container (data dir: $TP_TEST_DATA_DIR)"
$DOCKER run -d --name "$CONTAINER_NAME" \
  -p 127.0.0.1:18080:8080 \
  -v "$TP_TEST_DATA_DIR:/data" \
  -v "$KEY_FILE:/run/secrets/master_key:ro" \
  --tmpfs /tmp:mode=700,uid=10001,gid=10001 \
  -e TP_SETUP_RATE_LIMIT_PER_MIN=200 \
  -e TP_ALLOW_INSECURE_COOKIES=1 \
  "$IMAGE" >/dev/null

for i in $(seq 1 30); do
  if curl -fsS http://127.0.0.1:18080/healthz >/dev/null 2>&1; then
    break
  fi
  [ "$i" -eq 30 ] && { echo "e2e: /healthz never became ready"; exit 1; }
  sleep 1
done

echo "e2e: /healthz ok"
ready="$(curl -s http://127.0.0.1:18080/readyz)"
echo "$ready" | grep -q '"status":"ready"' || { echo "e2e: instance not ready: $ready"; exit 1; }
echo "$ready" | grep -q '"master_key":true' || { echo "e2e: master key check missing: $ready"; exit 1; }
echo "e2e: /readyz ready with master key check"
code="$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18080/api/v1/unknown)"
[ "$code" = "404" ] || { echo "e2e: unknown API returned $code, want 404"; exit 1; }
body="$(curl -s http://127.0.0.1:18080/api/v1/unknown)"
echo "$body" | grep -q '"code":"NOT_FOUND"' || { echo "e2e: unexpected 404 body: $body"; exit 1; }
echo "e2e: unknown API returns JSON 404"

page="$(curl -s http://127.0.0.1:18080/some/spa/route)"
echo "$page" | grep -qi "tiny password" || { echo "e2e: SPA route did not serve the app"; exit 1; }
echo "e2e: SPA route serves the app"

# ---- M1 acceptance: one-time initialization flow ---------------------------
BASE=http://127.0.0.1:18080
status="$(curl -s $BASE/api/v1/setup/status)"
echo "$status" | grep -q '"initialized":false' || { echo "e2e: expected uninitialized, got $status"; exit 1; }

token="$($DOCKER logs "$CONTAINER_NAME" 2>&1 | grep -o '"msg":"setup_token_issued","token":"[^"]*"' | head -1 | sed 's/.*"token":"//; s/"$//')"
[ -n "$token" ] || { echo "e2e: no setup token found in container logs"; exit 1; }
echo "e2e: setup token retrieved from logs (${#token} chars)"

init_count() ( # init_count <username> -> HTTP status code; isolate each caller
  request_dir="$(mktemp -d "$REQUEST_DIR/request.XXXXXX")"
  csrf="$(curl -fsS -c "$request_dir/cookies" -X POST $BASE/api/v1/csrf | grep -o '"csrf_token":"[^"]*"' | cut -d'"' -f4)"
  curl -sS -o "$request_dir/body" -w '%{http_code}' -b "$request_dir/cookies" \
    -H "Content-Type: application/json" \
    -H "X-CSRF-Token: $csrf" \
    -H "Origin: $BASE" \
    -d "{\"token\":\"$token\",\"username\":\"$1\",\"password\":\"correct horse battery 42\"}" \
    $BASE/api/v1/setup/init
)

code="$(init_count admin)"
[ "$code" = "200" ] || { echo "e2e: setup init returned $code, want 200"; exit 1; }
status="$(curl -s $BASE/api/v1/setup/status)"
echo "$status" | grep -q '"initialized":true' || { echo "e2e: status after init: $status"; exit 1; }
echo "e2e: one-time initialization succeeded"

code="$(init_count secondtry)"
[ "$code" = "409" ] || { echo "e2e: second init returned $code, want 409"; exit 1; }
echo "e2e: setup entry point closed after success"

# Restart the container: entry must stay closed, no new token may be issued.
$DOCKER restart "$CONTAINER_NAME" >/dev/null
for i in $(seq 1 30); do
  curl -fsS $BASE/healthz >/dev/null 2>&1 && break
  [ "$i" = 30 ] && { echo "e2e: container did not come back"; exit 1; }
  sleep 1
done
token_count="$($DOCKER logs "$CONTAINER_NAME" 2>&1 | grep -c 'setup_token_issued')"
[ "$token_count" = "1" ] || { echo "e2e: token issued $token_count times across restart, want exactly 1"; exit 1; }
code="$(init_count threentry)"
[ "$code" = "409" ] || { echo "e2e: setup after restart returned $code, want 409"; exit 1; }
echo "e2e: setup stays closed after restart; no new token issued"

# Concurrency: exactly one of N racing inits may succeed.
$DOCKER rm -f "$CONTAINER_NAME" >/dev/null
rm -rf "$TP_TEST_DATA_DIR"; mkdir -p "$TP_TEST_DATA_DIR"; chmod 0777 "$TP_TEST_DATA_DIR"
$DOCKER run -d --name "$CONTAINER_NAME" \
  -p 127.0.0.1:18080:8080 \
  -v "$TP_TEST_DATA_DIR:/data" \
  -v "$KEY_FILE:/run/secrets/master_key:ro" \
  --tmpfs /tmp:mode=700,uid=10001,gid=10001 \
  -e TP_SETUP_RATE_LIMIT_PER_MIN=200 \
  -e TP_ALLOW_INSECURE_COOKIES=1 \
  "$IMAGE" >/dev/null
for i in $(seq 1 30); do
  curl -fsS $BASE/healthz >/dev/null 2>&1 && break
  [ "$i" = 30 ] && { echo "e2e: second container not ready"; exit 1; }
  sleep 1
done
token="$($DOCKER logs "$CONTAINER_NAME" 2>&1 | grep -o '"msg":"setup_token_issued","token":"[^"]*"' | head -1 | sed 's/.*"token":"//; s/"$//')"
oks=0
conflicts=0
pids=()
for i in $(seq 1 8); do
  init_count "race$i" > "$REQUEST_DIR/race$i.status" &
  pids+=("$!")
done
for pid in "${pids[@]}"; do
  wait "$pid"
done
for i in $(seq 1 8); do
  code="$(cat "$REQUEST_DIR/race$i.status")"
  case "$code" in
    200) oks=$((oks+1)) ;;
    409) conflicts=$((conflicts+1)) ;;
    *) echo "e2e: racer $i returned $code, want 200 or 409"; exit 1 ;;
  esac
done
[ "$oks" = "1" ] && [ "$conflicts" = "7" ] || { echo "e2e: concurrent init produced $oks successes and $conflicts conflicts, want 1 and 7"; exit 1; }
echo "e2e: concurrent initialization: exactly 1 success and 7 conflicts"

echo "e2e: PASS"
