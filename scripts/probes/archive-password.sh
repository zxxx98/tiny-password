#!/usr/bin/env bash
# T01 probe: verify that the pinned 7-Zip CLI can receive archive passphrases
# through stdin without exposing them in argv, environment, terminal echo,
# or command output.
#
# Established protocol (validated by this probe, see docs/decisions/0001):
#   create : 7zz a -t7z -mhe=on -p <archive> <files>   (bare -p REQUIRED;
#            without it 7zz silently creates an UNENCRYPTED archive)
#   list   : 7zz l -slt <archive>                      (NO -p flag: bare -p is
#            treated as an empty password and fails without prompting)
#   test   : 7zz t <archive>
#   extract: 7zz x -y <archive> -o <dir>
#   For list/test/extract the passphrase is read from stdin only when needed.
#   Passphrase is always a single line (no LF/CR bytes) followed by EOF.
#
# Environment:
#   SEVENZIP_BIN  path to an existing 7zz binary (skips download)
#
# Exit 0 only when every assertion passes.

set -u -o pipefail

SEVENZIP_VERSION="26.03"
SEVENZIP_DIR_URL="https://github.com/ip7z/7zip/releases/download/${SEVENZIP_VERSION}"
# SHA256 recorded 2026-09-05 from the downloaded artifacts; re-verify against an
# independent source before the release milestone (T30/T31).
SEVENZIP_SHA256_X64="dc99eff5008f1ab79bd7084c68513701547a808a89502bf4133683535ab3c695"
SEVENZIP_SHA256_ARM64="2389ba20e4d8295e8709c20b6263b69bd1ec4972fe38a04ad7a1badbf595b996"

FAILURES=0
PASSED=0

note() { printf '%s\n' "  $*"; }
pass() { printf 'PASS  %s\n' "$1"; PASSED=$((PASSED + 1)); }
fail() { printf 'FAIL  %s\n' "$1"; FAILURES=$((FAILURES + 1)); }

assert_exit0() { # assert_exit0 <label> <rc>
  if [ "$2" -eq 0 ]; then pass "$1"; else fail "$1 (rc=$2)"; fi
}
assert_nonzero() {
  if [ "$2" -ne 0 ]; then pass "$1"; else fail "$1 (unexpected rc=0)"; fi
}
assert_absent() { # assert_absent <label> <pattern> <files...>
  local label="$1" pattern="$2"; shift 2
  if grep -qF -- "$pattern" "$@" 2>/dev/null; then fail "$label (pattern found)"; else pass "$label"; fi
}

case "$(uname -m)" in
  x86_64) SEVENZIP_TARBALL="7z${SEVENZIP_VERSION/./}-linux-x64.tar.xz"; SEVENZIP_SHA256="$SEVENZIP_SHA256_X64" ;;
  aarch64) SEVENZIP_TARBALL="7z${SEVENZIP_VERSION/./}-linux-arm64.tar.xz"; SEVENZIP_SHA256="$SEVENZIP_SHA256_ARM64" ;;
  *) fail "unsupported architecture: $(uname -m)"; exit 1 ;;
esac

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cd "$WORK"

SEVENZZ="${SEVENZIP_BIN:-}"
if [ -z "$SEVENZZ" ]; then
  note "downloading pinned 7-Zip ${SEVENZIP_VERSION} (${SEVENZIP_TARBALL})"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -o 7z.tar.xz "${SEVENZIP_DIR_URL}/${SEVENZIP_TARBALL}" || { fail "download of pinned 7-Zip failed"; exit 1; }
  elif command -v wget >/dev/null 2>&1; then
    wget -qO 7z.tar.xz "${SEVENZIP_DIR_URL}/${SEVENZIP_TARBALL}" || { fail "download of pinned 7-Zip failed"; exit 1; }
  else
    fail "no curl/wget available and SEVENZIP_BIN not set"; exit 1
  fi
  if ! printf '%s  7z.tar.xz\n' "$SEVENZIP_SHA256" | sha256sum -c - >/dev/null 2>&1; then
    fail "SHA256 mismatch for ${SEVENZIP_TARBALL}"; exit 1
  fi
  if ! tar xf 7z.tar.xz 7zz 2>/dev/null; then fail "untar failed (xz missing?)"; exit 1; fi
  chmod +x 7zz
  SEVENZZ="$WORK/7zz"
fi
note "using binary: $SEVENZZ"
"$SEVENZZ" i >/dev/null 2>&1 || { fail "7zz not executable"; exit 1; }

# Synthetic data; marker strings must never appear in archive bytes or output.
SECRET_MARKER="tiny-password-probe-secret-5f4a1b"
printf '%s\n' "$SECRET_MARKER" > secret.txt
PW_ASCII='pv-probe-4X9t!#$%^&*() qZ'
PW_UNICODE='påsswörd-探针-🔒-8kN#2'
ANY_PW="$PW_ASCII"

### 1. create via stdin pipe (bare -p, header encryption on) ###################
rm -f out.7z
# shellcheck disable=SC2086  # password intentionally not part of the command line
printf '%s\n' "$ANY_PW" | timeout 60 "$SEVENZZ" a -y -t7z -mhe=on -mx5 -p out.7z secret.txt >create.log 2>&1
assert_exit0 "create: exit 0 via stdin pipe (bare -p)" $?
grep -q "Everything is Ok" create.log && pass "create: reports 'Everything is Ok'" || fail "create: missing success line"
assert_absent "create: passphrase absent from stdout/stderr" "$ANY_PW" create.log

### 2. encryption assertions ##################################################
timeout 30 "$SEVENZZ" l -slt out.7z </dev/null >list_nopw.log 2>&1
assert_nonzero "header encryption: listing without passphrase fails" $?
assert_absent "archive bytes: payload marker absent" "$SECRET_MARKER" out.7z
assert_absent "archive bytes: file name absent (-mhe=on)" "secret.txt" out.7z
printf '%s\n' "$ANY_PW" | timeout 30 "$SEVENZZ" l -slt out.7z >list_ok.log 2>&1
assert_exit0 "list with passphrase via stdin pipe: exit 0" $?
grep -q '^Path = secret.txt' list_ok.log && pass "list: encrypted file name resolved" || fail "list: file name missing"
grep -q 'AES' list_ok.log && pass "list: payload uses AES" || fail "list: AES not reported"
assert_absent "list: passphrase absent from output" "$ANY_PW" list_ok.log

### 3. /proc sweep during a long create ######################################
dd if=/dev/urandom of=bulk.bin bs=1M count=48 status=none
# shellcheck disable=SC2086  # password intentionally not part of the command line
printf '%s\n' "$ANY_PW" | timeout 90 "$SEVENZZ" a -y -t7z -mhe=on -p out2.7z bulk.bin secret.txt >create2.log 2>&1 &
CREATE_PID=$!
LEAKED=0
while kill -0 "$CREATE_PID" 2>/dev/null; do
  for p in $(pgrep -P "$CREATE_PID" 2>/dev/null; echo "$CREATE_PID"); do
    [ -r "/proc/$p/cmdline" ] && tr '\0' '\n' <"/proc/$p/cmdline" 2>/dev/null | grep -qF -- "$ANY_PW" && LEAKED=1
    [ -r "/proc/$p/environ" ] && tr '\0' '\n' <"/proc/$p/environ" 2>/dev/null | grep -qF -- "$ANY_PW" && LEAKED=1
  done
  sleep 0.2
done
wait "$CREATE_PID"; CREATE2_RC=$?
assert_exit0 "long create: exit 0" "$CREATE2_RC"
if [ "$LEAKED" -eq 0 ]; then pass "/proc cmdline+environ sweep: passphrase never present"; else fail "/proc sweep: passphrase exposed"; fi

### 4. test + extract via stdin pipe #########################################
# shellcheck disable=SC2086
printf '%s\n' "$ANY_PW" | timeout 60 "$SEVENZZ" t -y out.7z >test.log 2>&1
assert_exit0 "test (t): exit 0 via stdin pipe" $?
rm -rf ex && mkdir ex
# shellcheck disable=SC2086
printf '%s\n' "$ANY_PW" | timeout 60 "$SEVENZZ" x -y out.7z -oex >extract.log 2>&1
assert_exit0 "extract: exit 0 via stdin pipe (no -p flag)" $?
grep -qF "$SECRET_MARKER" ex/secret.txt 2>/dev/null && pass "extract: content matches original" || fail "extract: content mismatch"
assert_absent "extract: passphrase absent from output" "$ANY_PW" extract.log

### 5. wrong passphrase behavior #############################################
rm -rf exw && mkdir exw
# shellcheck disable=SC2086
printf '%s\n' "definitely-wrong-passphrase" | timeout 60 "$SEVENZZ" x -y out.7z -oexw >wrong.log 2>&1
assert_nonzero "wrong passphrase: extraction fails" $?
if [ -e exw/secret.txt ]; then fail "wrong passphrase: file was written"; else pass "wrong passphrase: no file written"; fi
rm -rf exw2 && mkdir exw2
# shellcheck disable=SC2086
printf '%s\n' "definitely-wrong-passphrase" | timeout 60 "$SEVENZZ" x -y out2.7z -oexw2 >wrong2.log 2>&1
assert_nonzero "wrong passphrase on second archive: fails" $?

### 6. closed stdin: must fail fast, never hang ##############################
rm -rf exc && mkdir exc
timeout 30 "$SEVENZZ" x -y out.7z -oexc </dev/null >closed.log 2>&1
CLOSED_RC=$?
if [ "$CLOSED_RC" -eq 124 ]; then fail "closed stdin: process hung (timeout)"; else pass "closed stdin: fails fast (rc=$CLOSED_RC)"; fi

### 7. unicode passphrase roundtrip ##########################################
rm -f uni.7z; rm -rf exu && mkdir exu
# shellcheck disable=SC2086
printf '%s\n' "$PW_UNICODE" | timeout 60 "$SEVENZZ" a -y -t7z -mhe=on -p uni.7z secret.txt >uni_create.log 2>&1
assert_exit0 "unicode passphrase: create exit 0" $?
# shellcheck disable=SC2086
printf '%s\n' "$PW_UNICODE" | timeout 60 "$SEVENZZ" x -y uni.7z -oexu >uni_x.log 2>&1
assert_exit0 "unicode passphrase: extract exit 0" $?
grep -qF "$SECRET_MARKER" exu/secret.txt 2>/dev/null && pass "unicode passphrase: content matches" || fail "unicode passphrase: content mismatch"
assert_absent "unicode passphrase: absent from all captured output" "$PW_UNICODE" uni_create.log uni_x.log

### 8. create without -p must NOT be used: it silently skips encryption ######
rm -f unsafe.7z
# shellcheck disable=SC2086
printf '%s\n' "$ANY_PW" | timeout 60 "$SEVENZZ" a -y -t7z -mhe=on unsafe.7z secret.txt >/dev/null 2>&1
printf '%s\n' "$ANY_PW" | timeout 30 "$SEVENZZ" t -y unsafe.7z >/dev/null 2>&1
assert_exit0 "guard: create without -p yields an unencrypted archive (must never be used)" $?

printf '\n%d passed, %d failed\n' "$PASSED" "$FAILURES"
[ "$FAILURES" -eq 0 ]
