#!/bin/sh
# Install a nodr release archive from GitHub.
#
# Downloads the archive and checksums.txt for a release tag, verifies the
# SHA-256 checksum, and installs the nodr binary. The script never creates
# a workspace, secrets, or services: point nodr at an existing workspace
# yourself, as documented in the README and docs/guides/production.md.
#
# Usage:
#   install.sh [--version TAG] [--arch amd64|arm64] [--prefix DIR]
#              [--token TOKEN] [--force]
#
# Environment:
#   NODR_INSTALL_LOCAL_TEST=1  install from ./dist instead of GitHub
#                              (a test hook, not a supported feature)
set -eu

VERSION=""
ARCH=""
PREFIX="/usr/local"
TOKEN=""
FORCE=0

while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --arch) ARCH="$2"; shift 2 ;;
    --prefix) PREFIX="$2"; shift 2 ;;
    --token) TOKEN="$2"; shift 2 ;;
    --force) FORCE=1; shift ;;
    *) echo "install.sh: unknown option: $1" >&2; exit 2 ;;
  esac
done

if [ -z "$ARCH" ]; then
  case "$(uname -m)" in
    x86_64|amd64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) echo "install.sh: unsupported architecture: $(uname -m)" >&2; exit 1 ;;
  esac
fi
case "$ARCH" in amd64|arm64) ;; *) echo "install.sh: unsupported arch: $ARCH" >&2; exit 1 ;; esac

fetch() {
  url="$1" out="$2"
  if [ -n "$TOKEN" ]; then
    curl -fsSL -H "Authorization: Bearer $TOKEN" -o "$out" "$url"
  else
    curl -fsSL -o "$out" "$url"
  fi
}

TMPDIR_INSTALL="$(mktemp -d)"
trap 'rm -rf "$TMPDIR_INSTALL"' EXIT

if [ "${NODR_INSTALL_LOCAL_TEST:-0}" = "1" ]; then
  # Install from a GoReleaser snapshot in dist/ instead of GitHub. The archive
  # is copied under the release name so the checksum lookup below matches the
  # real release layout.
  VERSION="local"
  ARCHIVE_NAME="nodr_${VERSION#v}_linux_${ARCH}.tar.gz"
  cp dist/nodr_*_${ARCH}.tar.gz "$TMPDIR_INSTALL/${ARCHIVE_NAME}"
  ARCHIVE="$TMPDIR_INSTALL/${ARCHIVE_NAME}"
else
  if [ -z "$VERSION" ]; then
    echo "install.sh: --version is required (or set NODR_INSTALL_LOCAL_TEST=1)" >&2
    exit 2
  fi
  REPO="centopw/nodr"
  BASE="https://github.com/${REPO}/releases/download/${VERSION}"
  echo "Downloading nodr ${VERSION} for linux/${ARCH}..."
  ARCHIVE_NAME="nodr_${VERSION#v}_linux_${ARCH}.tar.gz"
  fetch "${BASE}/${ARCHIVE_NAME}" "$TMPDIR_INSTALL/${ARCHIVE_NAME}"
  fetch "${BASE}/checksums.txt" "$TMPDIR_INSTALL/checksums.txt"
fi

echo "Verifying the archive checksum..."
if [ "${NODR_INSTALL_LOCAL_TEST:-0}" = "1" ]; then
  shasum -a 256 "$ARCHIVE" | sed "s|  .*/|  |" > "$TMPDIR_INSTALL/checksums.txt"
fi
WANT="$(grep " $(basename "$ARCHIVE")$" "$TMPDIR_INSTALL/checksums.txt" | awk '{print $1}')"
if [ -z "$WANT" ]; then
  echo "install.sh: checksum entry missing for $(basename "$ARCHIVE")" >&2
  exit 1
fi
if ! echo "$WANT  $ARCHIVE" | shasum -a 256 -c - >/dev/null 2>&1; then
  echo "install.sh: checksum mismatch for $(basename "$ARCHIVE")" >&2
  exit 1
fi

tar -xzf "$ARCHIVE" -C "$TMPDIR_INSTALL" nodr

DEST="$PREFIX/bin/nodr"
if [ -e "$DEST" ] && [ "$FORCE" -ne 1 ]; then
  echo "install.sh: $DEST already exists (use --force to overwrite)" >&2
  exit 1
fi

mkdir -p "$PREFIX/bin"
install -m 0755 "$TMPDIR_INSTALL/nodr" "$DEST"
echo "Installed nodr at $DEST"
