#!/usr/bin/env bash
set -euo pipefail

# `docker compose config` validates interpolation, but it does not require
# the source files for Compose secrets to exist. Run this gate before config or
# deployment so a rendered-but-unstartable stack cannot be mistaken for a
# validated deployment.
check_file() {
  local label="$1" path="$2"
  if [[ ! -f "$path" || ! -r "$path" ]]; then
    echo "compose secret check failed: ${label} is not a readable regular file" >&2
    exit 1
  fi
}

check_file master_key "${TP_MASTER_KEY_SOURCE:-./secrets/master_key}"
check_file backup_passphrase "${TP_BACKUP_PASSPHRASE_SOURCE:-./secrets/backup_passphrase}"

if [[ "${TP_CHECK_TUNNEL:-0}" == "1" ]]; then
  check_file tunnel_token "${TP_TUNNEL_TOKEN_SOURCE:-./secrets/tunnel_token}"
fi

if [[ "${TP_CHECK_R2:-0}" == "1" ]]; then
  check_file r2_access_key "${TP_R2_ACCESS_KEY_SOURCE:-./secrets/r2_access_key}"
  check_file r2_secret_key "${TP_R2_SECRET_KEY_SOURCE:-./secrets/r2_secret_key}"
fi

echo "compose secret check passed"
