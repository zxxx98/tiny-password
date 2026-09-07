#!/usr/bin/env bash
set -euo pipefail

repeats="${TP_SEARCH_REPEATS:-5}"
target_ms="${TP_SEARCH_P95_TARGET_MS:-300}"
[[ "$repeats" =~ ^[1-9][0-9]*$ ]] || { echo "TP_SEARCH_REPEATS must be a positive integer" >&2; exit 2; }
[[ "$target_ms" =~ ^[1-9][0-9]*$ ]] || { echo "TP_SEARCH_P95_TARGET_MS must be a positive integer" >&2; exit 2; }

echo "search benchmark: ${repeats} runs, 10,000 encrypted items per run"
samples=()
for run in $(seq 1 "$repeats"); do
  echo "--- run ${run}/${repeats} ---"
  output="$(go test ./tests/integration -run '^TestSearchPerformanceBaseline$' -count=1 -v 2>&1)" || {
    printf '%s\n' "$output"
    exit 1
  }
  printf '%s\n' "$output"
  elapsed="$(printf '%s\n' "$output" | sed -n 's/.*search over 10000 items: \([0-9.]*\)s.*/\1/p' | tail -1)"
  if [[ -z "$elapsed" ]]; then
    echo "search benchmark failed: could not parse elapsed time" >&2
    exit 1
  fi
  samples+=("$(awk -v seconds="$elapsed" 'BEGIN { printf "%.3f", seconds * 1000 }')")
done

p95="$(printf '%s\n' "${samples[@]}" | sort -n | awk '
  { values[NR] = $1 }
  END {
    pos = int(NR * 0.95)
    if (pos < NR * 0.95) pos++
    if (pos < 1) pos = 1
    print values[pos]
  }
')"
echo "search benchmark P95: ${p95} ms (target <= ${target_ms} ms)"
if awk -v p95="$p95" -v target="$target_ms" 'BEGIN { exit !(p95 > target) }'; then
  echo "search benchmark failed: P95 exceeds target" >&2
  exit 1
fi
echo "search benchmark passed; retain the full output with CPU, memory, architecture, and cache state"
