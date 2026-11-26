#!/bin/sh
# Check that a macOS executable asks for none of the rights a person must
# grant in System Settings (Accessibility, Screen Recording, posting
# events to other apps): it references none of the calls that need or ask
# for them (review B3-m18). The released app only gives VoiceOver its
# items' names, which needs no right; tools/macmenu, CI's own tool, uses
# these calls and must fail this check (CI checks that too). macOS only
# (nm of the Xcode tools).
# Usage (in agent/): tools/check-macos-symbols.sh FILE
set -eu
f="$1"
fail() { echo "check-macos-symbols: $f: $*" >&2; exit 1; }
[ -f "$f" ] || fail "no such file"
syms=$(nm -u "$f" 2>&1) || fail "nm failed: $(printf '%s' "$syms" | head -c 300)"
[ -n "$syms" ] || fail "nm listed no undefined symbols"
bad=$(printf '%s\n' "$syms" | grep -Eo '_(AX[A-Za-z]+|CGPreflight[A-Za-z]*Access|CGRequest[A-Za-z]*Access|CGEventPost[A-Za-z]*|CGEventTapCreate[A-Za-z]*|CGWindowListCreateImage[A-Za-z]*|CGDisplayCreateImage[A-Za-z]*)$' | sort -u | tr '\n' ' ') || true
[ -z "$bad" ] || fail "references calls that need a right a person grants: $bad"
echo "check-macos-symbols: $f ok"
