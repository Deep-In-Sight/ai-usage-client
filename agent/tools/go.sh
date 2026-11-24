#!/bin/sh
# Run the Go toolchain inside the official golang Docker image.
# Usage: agent/tools/go.sh <go args...>
# The repository is mounted at /repo (agent/ is /repo/agent) so tests can
# read files next to agent/, such as the GNOME extension's clawd.svg.
# GOOS, GOARCH, CGO_ENABLED, GOFLAGS and AI_USAGE_* (test switches) are
# passed through when set.
set -eu
here=$(cd "$(dirname "$0")/.." && pwd)
root=$(cd "$here/.." && pwd)
cache="${XDG_CACHE_HOME:-$HOME/.cache}/ai-usage-agent-go"
mkdir -p "$cache/mod" "$cache/build" "$cache/home"
case "$(pwd)" in "$here"*) wd="/repo/agent${PWD#"$here"}" ;; *) wd=/repo/agent ;; esac
envs=""
for v in GOOS GOARCH CGO_ENABLED GOFLAGS $(env | sed -n 's/^\(AI_USAGE_[A-Z0-9_]*\)=.*/\1/p'); do
  eval "val=\${$v:-}"
  [ -n "$val" ] && envs="$envs -e $v=$val"
done
# shellcheck disable=SC2086
exec docker run --rm --network host \
  -u "$(id -u):$(id -g)" \
  -e HOME=/gohome -e GOMODCACHE=/gocache/mod -e GOCACHE=/gocache/build $envs \
  -v "$cache/mod:/gocache/mod" -v "$cache/build:/gocache/build" -v "$cache/home:/gohome" \
  -v "$root:/repo" -w "$wd" \
  golang:1.27 go "$@"
