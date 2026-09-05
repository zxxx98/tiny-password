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
CONTAINER_NAME="tiny-password-e2e"
cleanup() {
  $DOCKER rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
  rm -rf "$TP_TEST_DATA_DIR"
}
trap cleanup EXIT

echo "e2e: starting test container (data dir: $TP_TEST_DATA_DIR)"
$DOCKER run -d --name "$CONTAINER_NAME" \
  -p 127.0.0.1:18080:8080 \
  -v "$TP_TEST_DATA_DIR:/data" \
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
code="$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18080/api/v1/unknown)"
[ "$code" = "404" ] || { echo "e2e: unknown API returned $code, want 404"; exit 1; }
body="$(curl -s http://127.0.0.1:18080/api/v1/unknown)"
echo "$body" | grep -q '"code":"NOT_FOUND"' || { echo "e2e: unexpected 404 body: $body"; exit 1; }
echo "e2e: unknown API returns JSON 404"

page="$(curl -s http://127.0.0.1:18080/some/spa/route)"
echo "$page" | grep -qi "tiny password" || { echo "e2e: SPA route did not serve the app"; exit 1; }
echo "e2e: SPA route serves the app"

echo "e2e: PASS"
