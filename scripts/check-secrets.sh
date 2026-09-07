#!/usr/bin/env bash
set -euo pipefail

# Scan only operator-supplied runtime paths. Scanning the source tree would
# match deliberate synthetic markers in tests and is not evidence about a
# running instance. Paths are colon-separated and must be explicit.
marker="${TP_SECRET_MARKER:-SYNSECRET}"
paths="${TP_SECRET_SCAN_PATHS:-}"
if [[ -z "$paths" ]]; then
  echo "secret scan failed: set TP_SECRET_SCAN_PATHS to every runtime path that must be scanned" >&2
  exit 1
fi

IFS=: read -r -a scan_paths <<< "$paths"
for path in "${scan_paths[@]}"; do
  if [[ -z "$path" || ! -e "$path" ]]; then
    echo "secret scan failed: configured path does not exist: ${path:-<empty>}" >&2
    exit 1
  fi
  set +e
  rg --hidden --no-messages --text --fixed-strings "$marker" "$path"
  scan_status=$?
  set -e
  case "$scan_status" in
    0)
      echo "secret scan failed: synthetic marker found under $path" >&2
      exit 1
      ;;
    1)
      ;;
    *)
      echo "secret scan failed: unable to scan $path (rg exit $scan_status)" >&2
      exit 1
      ;;
  esac
done

echo "secret scan passed: ${marker} absent from configured runtime paths"
