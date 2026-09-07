#!/usr/bin/env bash
set -euo pipefail

pattern="${TP_RESTORE_DRILL_PATTERN:-TestRestoreFullRoundTripWithNewKey|TestRestoreOverExistingInstancePreservesSnapshot|TestRestoreRejects|TestRestoreCrash}"
echo "restore drill: integration pattern ${pattern}"
SEVENZIP_BIN="${SEVENZIP_BIN:-/tmp/tp-7zz/7zz}" \
  go test ./tests/integration -run "${pattern}" -count=1 -timeout=15m -v
echo "restore drill passed; review the output for rollback, rekey, migration, and resume cases"
