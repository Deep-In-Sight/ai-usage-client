#!/bin/sh
# Run the Go toolchain inside the official golang Docker image.
# Usage: agent/tools/go.sh <go args...>   (agent/ is mounted at /src)
# GOOS, GOARCH, CGO_ENABLED, GOFLAGS are passed through when set.
set -eu
here=$(cd "$(dirname "$0")/.." && pwd)
cache="${XDG_CACHE_HOME:-$HOME/.cache}/claude-usage-agent-go"
mkdir -p "$cache/mod" "$cache/build" "$cache/home"
case "$(pwd)" in "$here"*) wd="/src${PWD#"$here"}" ;; *) wd=/src ;; esac
envs=""
for v in GOOS GOARCH CGO_ENABLED GOFLAGS; do
  eval "val=\${$v:-}"
  [ -n "$val" ] && envs="$envs -e $v=$val"
done
# shellcheck disable=SC2086
exec docker run --rm --network host \
  -u "$(id -u):$(id -g)" \
  -e HOME=/gohome -e GOMODCACHE=/gocache/mod -e GOCACHE=/gocache/build $envs \
  -v "$cache/mod:/gocache/mod" -v "$cache/build:/gocache/build" -v "$cache/home:/gohome" \
  -v "$here:/src" -w "$wd" \
  golang:1.27 go "$@"
