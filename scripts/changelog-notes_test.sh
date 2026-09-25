#!/bin/sh
# Tests changelog-notes.sh with the changelog in testdata/. CI runs them.
#
# Usage: scripts/changelog-notes_test.sh

set -u

cd "$(dirname "$0")/testdata" || exit 1
script=../changelog-notes.sh
tests=0
failures=0

fail() {
  failures=$((failures + 1))
  printf 'FAIL %s\n' "$1" >&2
}

# prints NAME WANT ARGS...: the script succeeds with ARGS, prints WANT and
# writes nothing to stderr.
prints() {
  name=$1 want=$2
  shift 2
  tests=$((tests + 1))
  # The dot keeps trailing newlines, which $(...) would remove.
  got=$("$script" "$@" 2>&1; status=$?; echo .; exit "$status")
  status=$?
  got=${got%.}
  if [ "$status" -ne 0 ]; then
    fail "$name: exit status $status
$got"
  elif [ "$got" != "$want" ]; then
    fail "$name: got
$got
want
$want"
  fi
}

# fails NAME MESSAGE ARGS...: the script fails with ARGS, prints nothing on
# stdout and MESSAGE on stderr.
fails() {
  name=$1 message=$2
  shift 2
  tests=$((tests + 1))
  if stdout=$("$script" "$@" 2>/dev/null); then
    fail "$name: exit status 0, want an error"
    return
  fi
  if [ -n "$stdout" ]; then
    fail "$name: stdout is not empty:
$stdout"
  fi
  stderr=$("$script" "$@" 2>&1 >/dev/null)
  case $stderr in
  *"$message"*) ;;
  *) fail "$name: stderr is \"$stderr\", want \"$message\"" ;;
  esac
}

prints "a version with subsections" "### Added

- A feature, described
  on two lines.

### Fixed

- A bug.
" 1.1.0

prints "a pre-release" "- A release candidate.
" 1.1.0-rc.1

prints "the unreleased changes" "### Added

- A feature that is not released yet.
" Unreleased

prints "the last section, from a file given as an argument" "- The first release, in the last section of the file.
" 0.1.0 ./CHANGELOG.md

fails "a missing version" 'CHANGELOG.md has no "## [2.0.0]" section' 2.0.0
fails "a prefix of a version" 'no "## [1.1]" section' 1.1
fails "a version with pattern characters" 'no "## [1.1.*]" section' '1.1.*'
fails "an empty section" 'the "## [0.2.0]" section of CHANGELOG.md is empty' 0.2.0
fails "a section with only headings" 'the "## [1.0.0]" section of CHANGELOG.md is empty' 1.0.0
fails "a missing file" "cannot read missing.md" 1.1.0 missing.md
fails "no arguments" "usage:"
fails "too many arguments" "usage:" 1.1.0 CHANGELOG.md extra

if [ "$failures" -ne 0 ]; then
  echo "$failures of $tests tests failed" >&2
  exit 1
fi
echo "changelog-notes.sh: $tests tests passed"
