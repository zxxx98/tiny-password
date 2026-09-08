#!/usr/bin/env bash
set -euo pipefail

for command_name in uname go getconf free df awk mktemp sleep; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    echo "scale benchmark requires command: $command_name" >&2
    exit 2
  fi
done

if [[ ! -r /proc/self/status ]]; then
  echo "scale benchmark requires Linux /proc process status" >&2
  exit 2
fi

echo "scale benchmark: 20 member accounts, 10,000 encrypted personal items"
echo "architecture: $(uname -m)"
echo "go: $(go version)"
echo "cpus: $(getconf _NPROCESSORS_ONLN)"
echo "host_memory: $(free -h | awk '/^Mem:/ {print $2}')"
echo "filesystem: $(df -h . | awk 'NR == 2 {print $1 " size=" $2 " avail=" $4}')"

work_dir="$(mktemp -d)"
cleanup() {
  if [[ -n "${test_pid:-}" ]] && kill -0 "$test_pid" 2>/dev/null; then
    kill "$test_pid" 2>/dev/null || true
    wait "$test_pid" 2>/dev/null || true
  fi
  rm -rf -- "$work_dir"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
test_bin="$work_dir/scale.test"
test_log="$work_dir/scale.log"
timeout_seconds="${TP_SCALE_TIMEOUT_SECONDS:-300}"
[[ "$timeout_seconds" =~ ^[1-9][0-9]*$ ]] || {
  echo "TP_SCALE_TIMEOUT_SECONDS must be a positive integer" >&2
  exit 2
}

go test -c -o "$test_bin" ./tests/integration
"$test_bin" -test.run '^TestScaleBaseline$' -test.count=1 -test.v >"$test_log" 2>&1 &
test_pid=$!
peak_rss_kb=0
peak_hwm_kb=0
start_seconds=$SECONDS
timed_out=0
while kill -0 "$test_pid" 2>/dev/null; do
  if (( SECONDS - start_seconds >= timeout_seconds )); then
    echo "scale benchmark timed out after ${timeout_seconds}s" >&2
    kill "$test_pid" 2>/dev/null || true
    timed_out=1
    break
  fi
  rss_kb="$(awk '/^VmRSS:/ {print $2}' "/proc/$test_pid/status" 2>/dev/null || true)"
  hwm_kb="$(awk '/^VmHWM:/ {print $2}' "/proc/$test_pid/status" 2>/dev/null || true)"
  if [[ "${rss_kb:-0}" =~ ^[0-9]+$ ]] && (( rss_kb > peak_rss_kb )); then
    peak_rss_kb=$rss_kb
  fi
  if [[ "${hwm_kb:-0}" =~ ^[0-9]+$ ]] && (( hwm_kb > peak_hwm_kb )); then
    peak_hwm_kb=$hwm_kb
  fi
  sleep 0.02
done
status=0
if (( timed_out )); then
  wait "$test_pid" 2>/dev/null || true
  status=124
else
  wait "$test_pid" || status=$?
fi
cat "$test_log"
if (( peak_rss_kb == 0 || peak_hwm_kb == 0 )); then
  echo "scale benchmark failed: no valid RSS sample was collected" >&2
  status=1
fi
echo "scale benchmark peak_rss_kb=$peak_rss_kb peak_hwm_kb=$peak_hwm_kb"
exit "$status"
