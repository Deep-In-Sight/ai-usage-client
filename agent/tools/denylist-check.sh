#!/bin/sh
# Print any tracked line (agent/, .github/), branch commit message or line in
# the branch's diffs that matches
# a pattern from the given file. Exit 1 if anything matched, 2 on a check error.
# Patterns are matched byte-wise (LC_ALL=C).
# Usage: agent/tools/denylist-check.sh <pattern file>
set -u
pat="$1"
[ -r "$pat" ] || { echo "pattern file not readable" >&2; exit 2; }
cd "$(git rev-parse --show-toplevel)" || exit 2
export LC_ALL=C
hits=0
git grep -nIE -f "$pat" HEAD -- agent .github
rc=$?; [ $rc -eq 0 ] && hits=1; [ $rc -gt 1 ] && exit 2
git log origin/main..HEAD --format=%B | grep -nE -f "$pat"
rc=$?; [ $rc -eq 0 ] && hits=1; [ $rc -gt 1 ] && exit 2
git log -p --format= origin/main..HEAD | grep -nE -f "$pat"
rc=$?; [ $rc -eq 0 ] && hits=1; [ $rc -gt 1 ] && exit 2
exit $hits
