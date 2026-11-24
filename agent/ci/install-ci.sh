#!/bin/sh
# CI stand-in for the installer the team server generates (the same contract:
# download the release file, check it against SHA256SUMS, write config.json,
# run "install --config" as a direct child; exit 0 or 10 means installed).
# fakeserver fills in @BASE@ and @KEY@. Fake values only.
# Everything runs inside main(); the last line calls it.
main() {
set -eu
die() { echo "install-ci: $*" >&2; exit 1; }
BASE='@BASE@'
KEY='@KEY@'
case "$(uname -s)" in
  Linux) FILE=ai-usage-linux-amd64 ;;
  Darwin) FILE=ai-usage-macos-universal ;;
  *) die "unsupported OS" ;;
esac
tmp=$(mktemp -d "$HOME/.ai-usage-install.XXXXXX" 2>/dev/null || mktemp -d) || die "no temporary folder"
trap 'rm -rf "$tmp"' EXIT
get() {
  if command -v curl >/dev/null 2>&1; then curl -fsSL -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then wget -q -O "$2" "$1"
  else die "need curl or wget"; fi
}
get "$BASE/files/$FILE" "$tmp/ai-usage" || die "download failed: $FILE"
get "$BASE/files/SHA256SUMS" "$tmp/SHA256SUMS" || die "download failed: SHA256SUMS"
want=$(awk -v f="$FILE" '$2 == f { print $1 }' "$tmp/SHA256SUMS")
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/ai-usage" | cut -d ' ' -f 1)
else
  got=$(shasum -a 256 "$tmp/ai-usage" | cut -d ' ' -f 1)
fi
[ -n "$want" ] && [ "$got" = "$want" ] || die "hash does not match SHA256SUMS"
chmod 700 "$tmp/ai-usage"
(umask 077; printf '{"server_urls":["%s"],"ingest_key":"%s"}\n' "$BASE" "$KEY" > "$tmp/config.json")
rc=0
"$tmp/ai-usage" install --config "$tmp/config.json" || rc=$?
case "$rc" in
  0|10) echo "install-ci: installed (exit code $rc)" ;;
  *) die "install failed (exit code $rc)" ;;
esac
}
main "$@"
