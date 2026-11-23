#!/bin/sh
# Build the release executables into agent/dist/ with the docker Go wrapper.
# Usage: agent/tools/build.sh [VERSION]   (default 0.0.0-dev)
# Symbols are kept and nothing is packed: plain executables are what
# antivirus heuristics expect from normal programs.
set -eu
here=$(cd "$(dirname "$0")/.." && pwd)
ver="${1:-0.0.0-dev}"
case "$ver" in
  *[!A-Za-z0-9.+-]*|"") echo "bad version: $ver" >&2; exit 2 ;;
esac
cd "$here"
mkdir -p dist
ld="-X github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/version.Version=$ver"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 tools/go.sh build -trimpath -ldflags "$ld" \
  -o dist/claude-usage-linux-amd64 ./cmd/claude-usage
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 tools/go.sh build -trimpath -ldflags "$ld" \
  -o dist/claude-usage-windows-amd64.exe ./cmd/claude-usage
(cd dist && sha256sum claude-usage-linux-amd64 claude-usage-windows-amd64.exe > SHA256SUMS)
