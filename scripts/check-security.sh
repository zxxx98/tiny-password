#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${TP_SECURITY_URL:-http://127.0.0.1:8080}"
BASE_URL="${BASE_URL%/}"
headers="$(curl --fail-with-body --silent --show-error --max-time 10 -D - -o /dev/null "$BASE_URL/healthz")"

require_header() {
  local name="$1" needle="$2"
  printf '%s\n' "$headers" | grep -Faiq "${name}: ${needle}" || {
    echo "security check failed: ${name} does not contain ${needle}" >&2
    exit 1
  }
}

require_header "content-security-policy" "default-src 'self'"
require_header "x-content-type-options" "nosniff"
require_header "referrer-policy" "no-referrer"
require_header "x-frame-options" "DENY"
if [[ "$BASE_URL" == https://* ]]; then
  require_header "strict-transport-security" "max-age=31536000"
fi

api_headers="$(curl --silent --show-error --max-time 10 -D - -o /dev/null "$BASE_URL/api/v1/unknown")"
printf '%s\n' "$api_headers" | grep -Faiq 'cache-control: no-store' || {
  echo "security check failed: API response is cacheable" >&2
  exit 1
}
if printf '%s\n' "$api_headers" | grep -Faiq 'access-control-allow-origin:'; then
  echo "security check failed: unexpected CORS header" >&2
  exit 1
fi

echo "security headers and API cache policy passed for ${BASE_URL}"
