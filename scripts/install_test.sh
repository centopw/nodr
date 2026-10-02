#!/bin/sh
# Regression tests for the release installer. Runs without network access.
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
FIXTURE="$TMP/fixture"
BIN="$TMP/bin"
PREFIX="$TMP/prefix"
CURL_LOG="$TMP/curl.log"
CHECKSUM_LOG="$TMP/checksum.log"
NO_CHECKSUM_BIN="$TMP/no-checksum-bin"
SHASUM_BIN="$TMP/shasum-bin"
mkdir -p "$FIXTURE" "$BIN" "$PREFIX"
mkdir -p "$NO_CHECKSUM_BIN"
mkdir -p "$SHASUM_BIN"
for command in awk basename cp dirname grep install mkdir mktemp rm tar; do
  command_path="$(command -v "$command")"
  ln -s "$command_path" "$NO_CHECKSUM_BIN/$command"
  ln -s "$command_path" "$SHASUM_BIN/$command"
done

cat > "$TMP/nodr" <<'EOF'
#!/bin/sh
echo nodr fixture
EOF
chmod +x "$TMP/nodr"
tar -czf "$FIXTURE/archive.tar.gz" -C "$TMP" nodr
CHECKSUM=$(shasum -a 256 "$FIXTURE/archive.tar.gz" | awk '{print $1}')
printf '%s  %s\n' "$CHECKSUM" 'nodr_0.2.0_linux_amd64.tar.gz' > "$FIXTURE/checksums.txt"

cat > "$BIN/curl" <<'EOF'
#!/bin/sh
set -eu
out=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
printf '%s\n' "$url" >> "$CURL_LOG"
case "$url" in
  */releases/latest)
    printf '%s\n' "${LATEST_JSON:-{\"tag_name\":\"v0.2.0\"}}" > "$out"
    ;;
  */releases/download/v0.2.0/nodr_0.2.0_linux_amd64.tar.gz)
    cp "$FIXTURE/archive.tar.gz" "$out"
    ;;
  */releases/download/v0.2.0/checksums.txt)
    cp "$FIXTURE/checksums.txt" "$out"
    ;;
  *)
    printf 'unexpected curl URL: %s\n' "$url" >&2
    exit 1
    ;;
esac
EOF
chmod +x "$BIN/curl"

cat > "$BIN/sha256sum" <<'EOF'
#!/bin/sh
printf 'sha256sum\n' >> "$CHECKSUM_LOG"
exec /usr/bin/shasum -a 256 "$@"
EOF
chmod +x "$BIN/sha256sum"

cat > "$BIN/shasum" <<'EOF'
#!/bin/sh
printf 'shasum %s\n' "$*" >> "$CHECKSUM_LOG"
exec /usr/bin/shasum "$@"
EOF
chmod +x "$BIN/shasum"
ln -s "$BIN/curl" "$SHASUM_BIN/curl"
ln -s "$BIN/shasum" "$SHASUM_BIN/shasum"
ln -s "$BIN/curl" "$NO_CHECKSUM_BIN/curl"

run_install() {
  : > "$CURL_LOG"
  : > "$CHECKSUM_LOG"
  PATH="$BIN:$PATH" CURL_LOG="$CURL_LOG" CHECKSUM_LOG="$CHECKSUM_LOG" \
    FIXTURE="$FIXTURE" "$@"
}

assert_file() {
  test -x "$1" || { printf 'expected executable: %s\n' "$1" >&2; exit 1; }
}

# Default installs resolve GitHub's stable latest release before downloading.
run_install sh "$ROOT/scripts/install.sh" --arch amd64 --prefix "$PREFIX/default"
assert_file "$PREFIX/default/bin/nodr"
grep -Fx 'https://api.github.com/repos/centopw/nodr/releases/latest' "$CURL_LOG" >/dev/null
grep -Fx 'https://github.com/centopw/nodr/releases/download/v0.2.0/nodr_0.2.0_linux_amd64.tar.gz' "$CURL_LOG" >/dev/null
grep -Fx 'https://github.com/centopw/nodr/releases/download/v0.2.0/checksums.txt' "$CURL_LOG" >/dev/null

# Prefer sha256sum when it is available.
grep -F 'sha256sum' "$CHECKSUM_LOG" >/dev/null

# Explicit versions do not need a latest-release API lookup.
run_install sh "$ROOT/scripts/install.sh" --version v0.2.0 --arch amd64 --prefix "$PREFIX/pinned"
assert_file "$PREFIX/pinned/bin/nodr"
if grep -F '/releases/latest' "$CURL_LOG" >/dev/null; then
  echo 'explicit version unexpectedly queried latest release' >&2
  exit 1
fi

# Missing tag_name fails before any release archive is requested.
: > "$CURL_LOG"
if PATH="$BIN:$PATH" CURL_LOG="$CURL_LOG" FIXTURE="$FIXTURE" LATEST_JSON='{}' sh "$ROOT/scripts/install.sh" --arch amd64 --prefix "$PREFIX/invalid" >"$TMP/invalid.out" 2>&1; then
  echo 'malformed latest release response unexpectedly succeeded' >&2
  exit 1
fi
grep -F 'could not determine the latest release version' "$TMP/invalid.out" >/dev/null
if grep -F '/releases/download/' "$CURL_LOG" >/dev/null; then
  echo 'malformed latest response requested release assets' >&2
  exit 1
fi

# Fall back to shasum on platforms without sha256sum.
rm "$BIN/sha256sum"
: > "$CURL_LOG"
: > "$CHECKSUM_LOG"
PATH="$SHASUM_BIN" CURL_LOG="$CURL_LOG" CHECKSUM_LOG="$CHECKSUM_LOG" \
  FIXTURE="$FIXTURE" /bin/sh "$ROOT/scripts/install.sh" --version v0.2.0 --arch amd64 \
  --prefix "$PREFIX/shasum"
assert_file "$PREFIX/shasum/bin/nodr"
grep -F 'shasum -a 256' "$CHECKSUM_LOG" >/dev/null

# An environment with neither checksum program fails with a clear error.
: > "$CURL_LOG"
if PATH="$NO_CHECKSUM_BIN" CURL_LOG="$CURL_LOG" CHECKSUM_LOG="$CHECKSUM_LOG" \
  FIXTURE="$FIXTURE" /bin/sh "$ROOT/scripts/install.sh" --version v0.2.0 --arch amd64 \
  --prefix "$PREFIX/no-checksum" >"$TMP/no-checksum.out" 2>&1; then
  echo 'missing checksum tools unexpectedly succeeded' >&2
  exit 1
fi
grep -F 'sha256sum or shasum is required' "$TMP/no-checksum.out" >/dev/null

# An unwritable prefix fails fast with an actionable error.
RO_PREFIX="$TMP/ro-prefix"
mkdir -p "$RO_PREFIX"
chmod 555 "$RO_PREFIX"
: > "$CURL_LOG"
if PATH="$BIN:$PATH" CURL_LOG="$CURL_LOG" FIXTURE="$FIXTURE" sh "$ROOT/scripts/install.sh" \
  --version v0.2.0 --arch amd64 --prefix "$RO_PREFIX" >"$TMP/ro.out" 2>&1; then
  echo 'unwritable prefix unexpectedly succeeded' >&2
  exit 1
fi
grep -F 'cannot write to' "$TMP/ro.out" >/dev/null
grep -F 'sudo' "$TMP/ro.out" >/dev/null
