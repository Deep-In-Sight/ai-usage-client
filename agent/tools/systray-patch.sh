#!/bin/sh
# The in-repo copy agent/third_party/systray must be fyne.io/systray v1.12.2
# (as the Go checksum database knows it) plus exactly the change recorded
# in agent/third_party/systray.patch. This script downloads the upstream
# module, checks its hash, diffs it against the copy and compares the diff
# with the recorded one. Upstream's example/, Makefile and .gitignore are
# left out of the copy (PATCHED.md).
# Usage (in agent/): tools/systray-patch.sh           check (CI)
#                    tools/systray-patch.sh --write   record the current diff
set -eu
cd "$(dirname "$0")/.."
. tools/version.sh
GO=$(go_cmd)
want_sum='h1:Y8DZxgLHsVQt6rY9Zrkkg+j67S7vv/1F2viOWKPpVeA='
recorded=third_party/systray.patch

json=$($GO mod download -json fyne.io/systray@v1.12.2)
field() { printf '%s\n' "$json" | sed -n "s/^[[:space:]]*\"$1\": \"\(.*\)\",\{0,1\}$/\1/p"; }
sum=$(field Sum)
dir=$(field Dir)
if [ "$sum" != "$want_sum" ]; then
  echo "fyne.io/systray v1.12.2 has hash $sum, want $want_sum" >&2
  exit 1
fi
if [ "$GO" = tools/go.sh ]; then # the module cache as the host sees it
  dir="${XDG_CACHE_HOME:-$HOME/.cache}/ai-usage-agent-go/mod${dir#/gocache/mod}"
fi
[ -f "$dir/systray_unix.go" ] || { echo "upstream not found at $dir" >&2; exit 2; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cp -R "$dir" "$tmp/a"
chmod -R u+w "$tmp/a"
rm -rf "$tmp/a/example" "$tmp/a/Makefile" "$tmp/a/.gitignore"
cp -R third_party/systray "$tmp/b"
# a diff with stable headers (no dates), in a fixed file order
(cd "$tmp" && LC_ALL=C diff -ruN a b) | sed -E 's/^((---|\+\+\+) [^\t]*)\t.*$/\1/' > "$tmp/now.patch" || true

if [ "${1:-}" = --write ]; then
  cp "$tmp/now.patch" "$recorded"
  echo "wrote $recorded ($(grep -c '^+++ ' "$recorded") files changed or added)"
  exit 0
fi
if ! cmp -s "$tmp/now.patch" "$recorded"; then
  echo "third_party/systray is not upstream v1.12.2 plus $recorded:" >&2
  diff -u "$recorded" "$tmp/now.patch" | head -40 >&2
  exit 1
fi
echo "third_party/systray = fyne.io/systray v1.12.2 ($sum) + $recorded"
