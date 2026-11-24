#!/bin/sh
# Build the universal macOS executable (arm64 + x86_64, macOS 12 or later)
# into agent/dist/. macOS runners only: the tray needs cgo (Cocoa).
# Usage (in agent/): tools/build-macos.sh VERSION
set -eu
here=$(cd "$(dirname "$0")/.." && pwd)
ver="${1:-}"
. "$here/tools/version.sh"
check_version "$ver"
cd "$here"
ld="-X github.com/Deep-In-Sight/ai-usage-client/agent/internal/version.Version=$ver"
slices=dist/.macos-slices
rm -rf "$slices"
mkdir -p "$slices"
trap 'rm -rf "$slices"' EXIT
export CGO_ENABLED=1 MACOSX_DEPLOYMENT_TARGET=12.0 GOOS=darwin
GOARCH=arm64 go build -trimpath -ldflags "$ld" -o "$slices/arm64" ./cmd/ai-usage
GOARCH=amd64 CC="clang -arch x86_64" go build -trimpath -ldflags "$ld" -o "$slices/x86_64" ./cmd/ai-usage
tools/check-binary.sh "$slices/arm64"
tools/check-binary.sh "$slices/x86_64"
for a in arm64 x86_64; do
  if ! vtool -arch "$a" -show-build "$slices/$a" | grep -Eq 'minos 12\.0$'; then
    echo "$a slice: minimum macOS is not 12.0" >&2
    exit 1
  fi
done
out=dist/ai-usage-macos-universal
lipo -create -output "$out" "$slices/arm64" "$slices/x86_64"
archs=$(lipo -archs "$out")
[ "$archs" = "x86_64 arm64" ] || { echo "universal file has: $archs" >&2; exit 1; }
v=$("./$out" version)
printf '%s\n' "$v" | grep -Eqx "ai-usage $(printf '%s' "$ver" | sed 's/\./\\./g') darwin/(arm64|amd64) tray" ||
  { echo "version says: $v" >&2; exit 1; }
echo "$out: $archs, $v"
