#!/usr/bin/env bash
# Browser E2E (plan T15/T16): builds the server, runs it against a throwaway
# data directory, seeds accounts/items through the real API, then drives the
# built frontend with Playwright. Everything is cleaned up on exit.
set -eu -o pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

E2E_SPEC="${1:-}"
if [ -z "$E2E_SPEC" ]; then
  echo "usage: $0 <spec-file-name>   e.g. auth.spec.ts" >&2
  exit 2
fi

WORK="$(mktemp -d /tmp/tiny-password-browser-e2e.XXXXXX)"
SERVER_PID=""
SERVER_FAILED=0
cleanup() {
  if [ -n "$SERVER_PID" ]; then
    kill "$SERVER_PID" 2>/dev/null || true
  fi
  if [ "$SERVER_FAILED" = "1" ]; then
    echo "browser-e2e: server log tail (last 40 lines)" >&2
    tail -40 "$WORK/server.log" >&2 || true
  fi
  if [ -n "${KEEP_WORK:-}" ]; then
    echo "browser-e2e: work dir kept at $WORK" >&2
  else
    rm -rf "$WORK"
  fi
}
trap cleanup EXIT

# Synthetic master key and backup passphrase (test only).
head -c 32 /dev/urandom > "$WORK/master_key"
chmod 0600 "$WORK/master_key"
printf '%s' "e2e-backup-passphrase-1" > "$WORK/backup_passphrase"
chmod 0600 "$WORK/backup_passphrase"

# A previous failed run may have left the server bound to the port.
pkill -f "tiny-password-e2e-bin" 2>/dev/null || true
sleep 0.5

# Bind to a free port so parallel or stale listeners never interfere.
E2E_PORT="$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')"
BASE="http://127.0.0.1:$E2E_PORT"
export TP_E2E_BASE_URL="$BASE"

echo "browser-e2e: building frontend"
npm --prefix "$ROOT/web" run build > /dev/null

if [ ! -x "$ROOT/node_modules/.bin/playwright" ]; then
  echo "browser-e2e: installing root Playwright dependency"
  npm ci --ignore-scripts > /dev/null
fi

echo "browser-e2e: building server"
go build -o "$WORK/tiny-password-e2e-bin" ./cmd/tiny-password

# The pinned 7zz from the probe download (the production image ships it on
# PATH; a bare host needs SEVENZIP_BIN pointing at it).
if [ -z "${SEVENZIP_BIN:-}" ] && [ -x /tmp/tp-7zz/7zz ]; then
  export SEVENZIP_BIN=/tmp/tp-7zz/7zz
fi

TP_ADDR="127.0.0.1:$E2E_PORT" \
TP_DATA_DIR="$WORK/data" \
TP_MASTER_KEY_FILE="$WORK/master_key" \
TP_BACKUP_PASSPHRASE_FILE="$WORK/backup_passphrase" \
TP_ALLOW_INSECURE_COOKIES="${TP_ALLOW_INSECURE_COOKIES:-1}" \
TP_AUTH_LOGIN_LIMIT_PER_MIN="${TP_AUTH_LOGIN_LIMIT_PER_MIN:-100}" \
  setsid "$WORK/tiny-password-e2e-bin" > "$WORK/server.log" 2>&1 &
SERVER_PID=$!

for _ in $(seq 1 60); do
  if curl -sf "$BASE/healthz" > /dev/null; then
    break
  fi
  sleep 0.5
done
curl -sf "$BASE/healthz" > /dev/null || { echo "browser-e2e: server did not start" >&2; tail -20 "$WORK/server.log" >&2; exit 1; }

# The one-time token appears at boot; give the log a moment and retry.
TOKEN=""
for _ in $(seq 1 20); do
  TOKEN=$(grep -o '"token":"[^"]*"' "$WORK/server.log" | head -1 | cut -d'"' -f4 || true)
  [ -n "$TOKEN" ] && break
  sleep 0.25
done

ADMIN_PW="e2e-admin-password-1"
MEMBER_PW="e2e-member-password-1"
SECOND_PW="e2e-second-password-1"
JAR="$WORK/cookies"

echo "browser-e2e: initializing instance"
if [ -z "$TOKEN" ]; then
  echo "browser-e2e: no setup token in log" >&2; tail -5 "$WORK/server.log" >&2; exit 1
fi
rm -f "$JAR"
T=$(curl -sf -X POST -c "$JAR" -H "Origin: $BASE" "$BASE/api/v1/csrf" | python3 -c "import json,sys;print(json.load(sys.stdin)['csrf_token'])")
INIT_OUT=$(curl -s -w "\nHTTP_CODE:%{http_code}" -X POST -b "$JAR" -c "$JAR" -H "Content-Type: application/json" -H "Origin: $BASE" \
  -H "X-CSRF-Token: $T" -d "{\"token\":\"$TOKEN\",\"username\":\"e2e-admin\",\"password\":\"$ADMIN_PW\"}" \
  "$BASE/api/v1/setup/init") || true
echo "browser-e2e: setup/init => $INIT_OUT" >&2

login() { # username password jar
  local jar="$3"
  rm -f "$jar"
  local t
  t=$(curl -sf -X POST -c "$jar" -H "Origin: $BASE" "$BASE/api/v1/csrf" | python3 -c "import json,sys;print(json.load(sys.stdin)['csrf_token'])")
  curl -sf -X POST -b "$jar" -c "$jar" -H "Content-Type: application/json" -H "Origin: $BASE" \
    -H "X-CSRF-Token: $t" -d "{\"username\":\"$1\",\"password\":\"$2\"}" "$BASE/api/v1/auth/login" > "$WORK/login.json"
  python3 -c "import json;print(json.load(open('$WORK/login.json'))['csrf_token'])" > "$WORK/token"
}

echo "browser-e2e: seeding member and items"
login "e2e-admin" "$ADMIN_PW" "$JAR"
CT=$(cat "$WORK/token")
curl -sf -X POST -b "$JAR" -c "$JAR" -H "Content-Type: application/json" -H "Origin: $BASE" \
  -H "X-CSRF-Token: $CT" -d '{"username":"e2e-member","initial_password":"e2e-member-initial-1"}' \
  "$BASE/api/v1/users" > /dev/null
for PAIR in "e2e-rotator:e2e-rotator-initial-1" "e2e-second:e2e-second-initial-1"; do
  NAME="${PAIR%%:*}"
  INITIAL="${PAIR##*:}"
  curl -sf -X POST -b "$JAR" -c "$JAR" -H "Content-Type: application/json" -H "Origin: $BASE" \
    -H "X-CSRF-Token: $CT" -d "{\"username\":\"$NAME\",\"initial_password\":\"$INITIAL\"}" \
    "$BASE/api/v1/users" > /dev/null
done

login "e2e-member" "e2e-member-initial-1" "$JAR"
CT=$(cat "$WORK/token")
curl -sf -X POST -b "$JAR" -c "$JAR" -H "Content-Type: application/json" -H "Origin: $BASE" \
  -H "X-CSRF-Token: $CT" -d '{"current_password":"e2e-member-initial-1","new_password":"'"$MEMBER_PW"'"}' \
  "$BASE/api/v1/auth/password" > /dev/null

# Second member for the shared-workspace spec.
login "e2e-second" "e2e-second-initial-1" "$JAR"
CT=$(cat "$WORK/token")
curl -sf -X POST -b "$JAR" -c "$JAR" -H "Content-Type: application/json" -H "Origin: $BASE"   -H "X-CSRF-Token: $CT" -d '{"current_password":"e2e-second-initial-1","new_password":"'"$SECOND_PW"'"}'   "$BASE/api/v1/auth/password" > /dev/null

# A shared item created by e2e-member.
login "e2e-member" "$MEMBER_PW" "$JAR"
CT=$(cat "$WORK/token")
curl -sf -X POST -b "$JAR" -c "$JAR" -H "Content-Type: application/json" -H "Origin: $BASE"   -H "X-CSRF-Token: $CT" -H "Idempotency-Key: e2e-seed-shared-1"   -d '{"item_type":"login","vault_scope":"shared","payload":{"name":"e2e shared login","username":"ops","password":"SYNSECRET-e2e-shared"},"tags":["e2e"]}'   "$BASE/api/v1/items" > /dev/null

login "e2e-member" "$MEMBER_PW" "$JAR"
CT=$(cat "$WORK/token")
curl -sf -X POST -b "$JAR" -c "$JAR" -H "Content-Type: application/json" -H "Origin: $BASE" \
  -H "X-CSRF-Token: $CT" -H "Idempotency-Key: e2e-seed-login-1" \
  -d '{"item_type":"login","vault_scope":"personal","payload":{"name":"e2e login","username":"alice","password":"SYNSECRET-e2e","urls":["https://e2e.example"]},"tags":["e2e"]}' \
  "$BASE/api/v1/items" > /dev/null

echo "browser-e2e: running Playwright spec $E2E_SPEC"
if TP_E2E_MEMBER_PW="$MEMBER_PW" TP_E2E_SECOND_PW="$SECOND_PW" \
  npx --no-install playwright test --workers=1 --config "$ROOT/playwright.config.ts" "$E2E_SPEC"; then
  echo "browser-e2e: PASS"
else
  SERVER_FAILED=1
  exit 1
fi
