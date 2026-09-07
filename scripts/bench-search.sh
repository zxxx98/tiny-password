#!/usr/bin/env bash
set -euo pipefail

repeats="${TP_SEARCH_REPEATS:-5}"
target_ms="${TP_SEARCH_P95_TARGET_MS:-300}"
[[ "$repeats" =~ ^[1-9][0-9]*$ ]] || { echo "TP_SEARCH_REPEATS must be a positive integer" >&2; exit 2; }
[[ "$target_ms" =~ ^[1-9][0-9]*$ ]] || { echo "TP_SEARCH_P95_TARGET_MS must be a positive integer" >&2; exit 2; }

echo "search benchmark: ${repeats} runs, 10,000 encrypted items per run"
records=()
for run in $(seq 1 "$repeats"); do
  echo "--- run ${run}/${repeats} ---"
  output="$(go test ./tests/integration -run '^TestSearchPerformanceMatrix$' -count=1 -v 2>&1)" || {
    printf '%s\n' "$output"
    exit 1
  }
  printf '%s\n' "$output"
  while IFS=$'\t' read -r query mode elapsed; do
    if [[ -n "$query" && -n "$mode" && -n "$elapsed" ]]; then
      records+=("${query}\t${mode}\t${elapsed}")
    fi
  done < <(printf '%s\n' "$output" | sed -n 's/.*search sample query="\([^"]*\)" mode=\([^ ]*\) elapsed_ms=\([0-9.]*\).*/\1\t\2\t\3/p')
done

if [[ "${#records[@]}" -eq 0 ]]; then
  echo "search benchmark failed: could not parse phase samples" >&2
  exit 1
fi

report="$(printf '%b\n' "${records[@]}" | awk -F '\t' '
function percentile(prefix, n,    i, j, tmp, pos) {
  for (i = 1; i <= n; i++) sorted[i] = values[prefix SUBSEP i]
  for (i = 1; i <= n; i++) {
    for (j = i + 1; j <= n; j++) {
      if (sorted[j] < sorted[i]) {
        tmp = sorted[i]
        sorted[i] = sorted[j]
        sorted[j] = tmp
      }
    }
  }
  pos = int(n * 0.95)
  if (pos < n * 0.95) pos++
  if (pos < 1) pos = 1
  return sorted[pos]
}
{
  key = $1 SUBSEP $2
  counts[key]++
  values[key SUBSEP counts[key]] = $3
  allCount++
  allValues[allCount] = $3
}
END {
  for (i = 1; i <= allCount; i++) values["__all__" SUBSEP i] = allValues[i]
  print "GLOBAL\tall\t" percentile("__all__", allCount)
  for (key in counts) {
    split(key, parts, SUBSEP)
    print parts[1] "\t" parts[2] "\t" percentile(key, counts[key])
  }
}')"

max_p95=0
while IFS=$'\t' read -r label mode p95; do
  [[ -n "$p95" ]] || continue
  echo "search benchmark P95 ${label} ${mode}: ${p95} ms (target <= ${target_ms} ms)"
  if awk -v p95="$p95" -v target="$target_ms" 'BEGIN { exit !(p95 > target) }'; then
    echo "search benchmark failed: P95 exceeds target for ${label} ${mode}" >&2
    exit 1
  fi
  if awk -v p95="$p95" -v max="$max_p95" 'BEGIN { exit !(p95 > max) }'; then
    max_p95="$p95"
  fi
done <<< "$report"
echo "search benchmark max P95: ${max_p95} ms (target <= ${target_ms} ms)"
echo "search benchmark passed; retain the full output with CPU, memory, architecture, and cache state"
