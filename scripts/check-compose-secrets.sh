#!/usr/bin/env bash
set -euo pipefail

# The app's master key and backup passphrase are initialized in the persistent
# data directory by the `init-secrets` service. This gate is only for optional
# Docker Secret files that are enabled through Compose overrides.
check_file() {
  local label="$1" path="$2"
  if [[ ! -f "$path" || ! -r "$path" ]]; then
    echo "compose secret check failed: ${label} is not a readable regular file" >&2
    exit 1
  fi
}

if [[ "${TP_CHECK_TUNNEL:-0}" == "1" ]]; then
  check_file tunnel_token "${TP_TUNNEL_TOKEN_SOURCE:-./secrets/tunnel_token}"
fi

if [[ "${TP_CHECK_R2:-0}" == "1" ]]; then
  check_file r2_access_key "${TP_R2_ACCESS_KEY_SOURCE:-./secrets/r2_access_key}"
  check_file r2_secret_key "${TP_R2_SECRET_KEY_SOURCE:-./secrets/r2_secret_key}"
fi

echo "compose secret check passed"
