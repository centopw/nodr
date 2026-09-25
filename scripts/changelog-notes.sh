#!/bin/sh
# Prints the release notes of one version from a changelog in the Keep a
# Changelog format: the body of its "## [VERSION]" section, without the
# heading and without leading or trailing blank lines. The release workflow
# publishes them with the GitHub Release.
#
# Usage: scripts/changelog-notes.sh VERSION [FILE]
#
# VERSION is written as in the heading, without a "v": 0.1.0, not v0.1.0.
# FILE defaults to CHANGELOG.md. The script fails if FILE has no section for
# VERSION, or if the section has nothing but headings.

set -eu

prog=${0##*/}

if [ $# -lt 1 ] || [ $# -gt 2 ] || [ -z "$1" ]; then
  echo "usage: $prog VERSION [FILE]" >&2
  exit 2
fi
version=$1
file=${2:-CHANGELOG.md}

if [ ! -f "$file" ] || [ ! -r "$file" ]; then
  echo "$prog: cannot read $file" >&2
  exit 1
fi

# awk prints the notes only if there are some. It exits with status 3 if the
# section is missing, and 4 if it is empty.
status=0
awk -v heading="## [$version]" '
  !found {
    # A string comparison, so that the dots of the version are not
    # patterns.
    if (index($0, heading) == 1) found = 1
    next
  }
  # The section ends at the heading of the next one.
  /^## / { exit }
  # Keep blank lines only between other lines.
  /^[ \t\r]*$/ {
    if (notes != "") blanks = blanks "\n"
    next
  }
  {
    notes = notes blanks $0 "\n"
    blanks = ""
    if ($0 !~ /^#+ /) entries = 1
  }
  END {
    if (!found) exit 3
    if (!entries) exit 4
    printf "%s", notes
  }
' "$file" || status=$?

case $status in
0) ;;
3)
  echo "$prog: $file has no \"## [$version]\" section" >&2
  exit 1
  ;;
4)
  echo "$prog: the \"## [$version]\" section of $file is empty" >&2
  exit 1
  ;;
*) exit "$status" ;;
esac
