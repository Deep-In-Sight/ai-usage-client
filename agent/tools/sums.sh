#!/bin/sh
# Write DIR/SHA256SUMS (sha256sum format) for the release files in DIR.
# Usage: tools/sums.sh DIR [--require-all]
set -eu
dir="$1"
all="${2:-}"
present=""
for n in claude-usage-linux-amd64 claude-usage-macos-universal claude-usage-windows-amd64.exe; do
  if [ -f "$dir/$n" ]; then
    present="$present $n"
  elif [ "$all" = "--require-all" ]; then
    echo "sums: $dir/$n is missing" >&2
    exit 1
  fi
done
[ -n "$present" ] || { echo "sums: no release files in $dir" >&2; exit 1; }
# shellcheck disable=SC2086
(cd "$dir" && sha256sum $present > SHA256SUMS)
cat "$dir/SHA256SUMS"
