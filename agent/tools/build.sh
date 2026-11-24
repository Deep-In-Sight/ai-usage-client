#!/bin/sh
# Build the Linux and Windows release executables into agent/dist/, check
# them and write dist/SHA256SUMS. Uses go when installed, else the docker
# wrapper. Usage (in agent/): tools/build.sh [VERSION]   (default 0.0.0-dev)
# Symbols are kept and nothing is packed: plain executables are what
# antivirus heuristics expect from normal programs.
set -eu
here=$(cd "$(dirname "$0")/.." && pwd)
ver="${1:-0.0.0-dev}"
. "$here/tools/version.sh"
check_version "$ver"
cd "$here"
GO=$(go_cmd)
filever="$(printf '%s' "$ver" | sed -E 's/^([0-9]+\.[0-9]+\.[0-9]+).*/\1/').0"
ld="-X github.com/Deep-In-Sight/ai-usage-client/agent/internal/version.Version=$ver"
mkdir -p dist

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $GO build -trimpath -ldflags "$ld" \
  -o dist/ai-usage-linux-amd64 ./cmd/ai-usage

# Windows: version information, manifest and icon as a .syso next to main.
trap 'rm -f cmd/ai-usage/rsrc_windows_amd64.syso winres/.build.json' EXIT
sed -e "s/@FILE_VERSION@/$filever/g" -e "s/@PRODUCT_VERSION@/$ver/g" winres/winres.json.in > winres/.build.json
$GO tool go-winres make --in winres/.build.json --out cmd/ai-usage/rsrc --arch amd64
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $GO build -trimpath -ldflags "-H=windowsgui $ld" \
  -o dist/ai-usage-windows-amd64.exe ./cmd/ai-usage

tools/check-binary.sh dist/ai-usage-linux-amd64
tools/check-binary.sh dist/ai-usage-windows-amd64.exe
tools/sums.sh dist
