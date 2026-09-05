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
KEY_FILE="$KEY_DIR/master_key"
head -c 32 /dev/urandom > "$KEY_FILE"
chmod 0644 "$KEY_FILE"  # container reads as UID 10001; synthetic key only
CONTAINER_NAME="tiny-password-e2e"
cleanup() {
  $DOCKER rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
  rm -rf "$TP_TEST_DATA_DIR" "$KEY_DIR"
}
trap cleanup EXIT

echo "e2e: starting test container (data dir: $TP_TEST_DATA_DIR)"
$DOCKER run -d --name "$CONTAINER_NAME" \
  -p 127.0.0.1:18080:8080 \
  -v "$TP_TEST_DATA_DIR:/data" \
  -v "$KEY_FILE:/run/secrets/master_key:ro" \
  --tmpfs /tmp:mode=700,uid=10001,gid=10001 \
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

echo "e2e: PASS"
