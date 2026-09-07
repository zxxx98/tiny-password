#!/usr/bin/env bash
set -euo pipefail

# Scan only operator-supplied runtime paths. Scanning the source tree would
# match deliberate synthetic markers in tests and is not evidence about a
# running instance. Paths are colon-separated and must be explicit.
marker="${TP_SECRET_MARKER:-SYNSECRET}"
paths="${TP_SECRET_SCAN_PATHS:-}"
if [[ -z "$paths" ]]; then
  echo "secret scan skipped: set TP_SECRET_SCAN_PATHS to data, tmp, log, and audit paths"
  exit 0
fi

for path in ${paths//:/ }; do
  [[ -e "$path" ]] || continue
  if rg --hidden --no-messages --text --fixed-strings "$marker" "$path"; then
    echo "secret scan failed: synthetic marker found under $path" >&2
    exit 1
  fi
done

echo "secret scan passed: ${marker} absent from configured runtime paths"
