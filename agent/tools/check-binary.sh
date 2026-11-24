#!/bin/sh
# Check one built agent executable: the right main package, -trimpath, no
# stripped symbols, only the allowed modules, not packed, and (Linux)
# statically linked or (Windows) a 64-bit GUI executable.
# Usage (in agent/): tools/check-binary.sh FILE
set -eu
here=$(cd "$(dirname "$0")/.." && pwd)
. "$here/tools/version.sh"
f="$1"
fail() { echo "check-binary: $f: $*" >&2; exit 1; }
[ -f "$f" ] || fail "no such file"
GO=$(go_cmd)
# tools/go.sh sees only the repository: give it a path relative to agent/.
case "$GO" in tools/go.sh) rel=$(cd "$(dirname "$f")" && pwd)/$(basename "$f"); rel=${rel#"$here"/}; cd "$here"; f=$rel ;; esac
info=$($GO version -m "$f") || fail "not a Go executable"
printf '%s\n' "$info" | grep -Eq '^[[:space:]]+path[[:space:]]+github\.com/Deep-In-Sight/ai-usage-client/agent/cmd/claude-usage$' ||
  fail "not built from agent/cmd/claude-usage"
printf '%s\n' "$info" | grep -Eq '^[[:space:]]+build[[:space:]]+-trimpath=true$' || fail "not built with -trimpath"
if printf '%s\n' "$info" | grep -E '^[[:space:]]+build[[:space:]]+-ldflags=' | grep -Eq '(^|[[:space:]"=])-[sw]([[:space:]"]|$)'; then
  fail "symbols stripped (-s or -w)"
fi
for m in $(printf '%s\n' "$info" | awk '$1 == "dep" { print $2 }'); do
  case "$m" in
    fyne.io/systray|github.com/godbus/dbus/v5|github.com/miekg/dns|golang.org/x/net|golang.org/x/sys) ;;
    *) fail "module $m is not allowed in the executable" ;;
  esac
done
if grep -aq 'UPX!' "$f"; then fail "packed (UPX)"; fi
kind=$(file -b "$f")
case "$kind" in
  ELF*) printf '%s' "$kind" | grep -q 'statically linked' || fail "not statically linked: $kind" ;;
  PE32*)
    case "$kind" in
      PE32+*GUI*x86-64*|PE32+*x86-64*GUI*) ;;
      *) fail "not a 64-bit Windows GUI executable: $kind" ;;
    esac ;;
  Mach-O*) ;;
  *) fail "unknown kind: $kind" ;;
esac
echo "check-binary: $f ok"
