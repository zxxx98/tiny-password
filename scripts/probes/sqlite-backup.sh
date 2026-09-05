#!/usr/bin/env bash
# T01 probe runner: builds and runs the SQLite online backup probe against the
# pinned driver version recorded in scripts/probes/sqlite-backup/go.mod.
#
# Exit 0 only when the probe passes.

set -eu -o pipefail

PROBE_DIR="$(cd "$(dirname "$0")/sqlite-backup" && pwd)"
cd "$PROBE_DIR"

go vet .
exec go run .
