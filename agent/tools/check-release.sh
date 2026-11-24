#!/bin/sh
# Check a Linux release executable before it is published: VERSION is a
# release's (not 0.0.0-ci or another test build's), the file reports
# exactly VERSION, and it honours none of the CI build's test switches: a
# run with every switch set logs no "CI build" line and runs no fake
# codex. Nothing is signed in for that run and its only server address
# refuses (127.0.0.1:9), so it asks no one.
# --rehearsal (ordinary CI, every push): the same checks on files built
# the same way under a version that can never be released (VERSION must
# be one release_version refuses, such as 0.8.99-relcheck), so a broken
# check shows up before a tag exists. tools/check-release.ps1 is the same
# for the Windows file.
# Usage: tools/check-release.sh [--rehearsal] FILE VERSION
set -eu
here=$(cd "$(dirname "$0")/.." && pwd)
. "$here/tools/version.sh"
rehearsal=0
if [ "${1:-}" = --rehearsal ]; then rehearsal=1; shift; fi
f="$1"
want="$2"
fail() { echo "check-release: $f: $*" >&2; exit 1; }
if [ $rehearsal = 1 ]; then
  if release_version "$want" 2>/dev/null; then fail "a rehearsal needs a version that can never be released, not $want"; fi
else
  release_version "$want" || fail "not a release version: $want"
fi
out=$("$f" version 2>&1) || fail "version failed: $out"
# "ai-usage <version> <os>/<arch> <tray word>": the version compared whole
got=$(printf '%s\n' "$out" | awk 'NR == 1 && $1 == "ai-usage" { print $2 }')
[ "$got" = "$want" ] || fail "reports [$out], not $want"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
data="$tmp/u/.config/ai-usage"
mkdir -p "$data"
printf '{"server_urls":["http://127.0.0.1:9"],"ingest_key":"check-release-key-0123456789"}\n' > "$data/config.json"
printf '#!/bin/sh\ntouch "%s/codex-ran"\n' "$tmp" > "$tmp/codex"
chmod 755 "$tmp/codex"
# every switch a CI build reads (internal/cli testSwitches)
env -i PATH=/usr/bin:/bin HOME="$tmp/u" \
  AI_USAGE_TEST_FAKE_PROVIDERS=claude,codex \
  AI_USAGE_TEST_DUMP_VIEW="$tmp/view.json" \
  AI_USAGE_TEST_CODEX_FALLBACK="$tmp/codex" \
  timeout -s TERM 6 "$f" run --headless > "$tmp/run.txt" 2>&1 || true
log="$data/agent.log"
grep -q "ai-usage $want started" "$log" 2>/dev/null || fail "the run did not start: $(head -c 300 "$tmp/run.txt")"
if grep -q 'CI build' "$log"; then
  fail "behaves as a CI build: $(grep 'CI build' "$log" | head -n 5 | tr '\n' ' ')"
fi
[ ! -e "$tmp/codex-ran" ] || fail "ran the fake codex"
[ ! -e "$tmp/view.json" ] || fail "wrote the test view"
echo "check-release: $f ok ($want, no test switch honoured)"
