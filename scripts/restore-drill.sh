#!/usr/bin/env bash
set -euo pipefail

pattern="${TP_RESTORE_DRILL_PATTERN:-^TestRestore}"
archive_pattern="${TP_ARCHIVE_DRILL_PATTERN:-^Test(ValidateEntries|ParseSLT)}"

ensure_tests_exist() {
  local package="$1" test_pattern="$2" listed matches
  listed="$(go test "$package" -list "$test_pattern" -count=1)"
  matches="$(printf '%s\n' "$listed" | grep -E "$test_pattern" || true)"
  if [[ -z "$matches" ]]; then
    echo "restore drill failed: pattern ${test_pattern@Q} matched no tests in ${package}" >&2
    exit 1
  fi
  printf '%s\n' "$matches"
}

echo "restore drill: integration pattern ${pattern}"
ensure_tests_exist ./tests/integration "$pattern"
SEVENZIP_BIN="${SEVENZIP_BIN:-/tmp/tp-7zz/7zz}" \
  go test ./tests/integration -run "${pattern}" -count=1 -timeout=15m -v

echo "restore drill: archive validation pattern ${archive_pattern}"
ensure_tests_exist ./internal/platform/archive "$archive_pattern"
go test ./internal/platform/archive -run "${archive_pattern}" -count=1 -timeout=5m -v

echo "restore drill passed; rollback, rekey, schema, space, archive, migration, and resume cases were selected"
