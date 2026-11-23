#!/usr/bin/env bash
# macOS runner checks: the sign-in only in the login keychain, one-line
# install, LaunchAgent, tray mode sync, no listening port, no token leak,
# in-place upgrade and uninstall. Results as annotations; exit 1 if a
# required check failed.
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=lib.sh
. "$here/lib.sh"

label=io.github.deep-in-sight.claude-usage
plist="$HOME/Library/LaunchAgents/$label.plist"
data="$HOME/Library/Application Support/ClaudeUsage"
exe="$data/claude-usage"
tmo() { perl -e 'alarm shift; exec @ARGV' "$@"; } # no timeout(1) on macOS
oneliner() { tmo 180 sh -c "curl -fsSL $BASE/files/install-ci.sh | sh"; }
agent_count() { pgrep -f "^$exe run" | wc -l | tr -d ' '; }
la_print() { launchctl print "gui/$(id -u)/$label" 2>&1; }
la_pid() { la_print | awk -F'= ' '/^[[:space:]]*pid = /{ print $2; exit }'; }
la_state() { la_print | awk -F'= ' '/^[[:space:]]*state = /{ print $2; exit }'; }

fake_login # account only: the sign-in is in the keychain
rm -f "$HOME/.claude/.credentials.json"
security add-generic-password -U -a "$USER" -s "Claude Code-credentials" -w "$(fake_creds_json)" > /dev/null 2>&1 ||
  fail keychain.setup "could not add the keychain item"
trap 'security delete-generic-password -s "Claude Code-credentials" > /dev/null 2>&1' EXIT
start_fakeserver || { fail fakeserver "did not start"; finish; exit 1; }

# install.oneline
oneliner > "$out/install.log" 2>&1
rc=$?
t_install=$(date +%s)
if [ $rc -eq 0 ] && [ -x "$exe" ] && grep -q "menu bar" "$out/install.log"; then pass install.oneline "rc=0"
else fail install.oneline "rc=$rc; $(tail -n 3 "$out/install.log")"; fi

# install.no_quarantine
if q=$(xattr -p com.apple.quarantine "$exe" 2>/dev/null); then fail install.no_quarantine "$q"; else pass install.no_quarantine "none"; fi

# binary.universal
archs=$(lipo -archs "$exe" 2>&1); v=$("$exe" version 2>&1)
case "$v" in *" tray") ok_v=1 ;; *) ok_v=0 ;; esac
if [ "$archs" = "x86_64 arm64" ] && [ $ok_v = 1 ]; then pass binary.universal "$archs; $v"; else fail binary.universal "archs=$archs version=$v"; fi

# launchagent.loaded
a0=$(plutil -extract ProgramArguments.0 raw "$plist" 2>&1); a1=$(plutil -extract ProgramArguments.1 raw "$plist" 2>&1)
running() { [ "$(la_state)" = running ]; }
wait_for 20 running
st=$(la_state)
if [ "$a0" = "$exe" ] && [ "$a1" = run ] && [ "$st" = running ]; then pass launchagent.loaded "state=$st"
else fail launchagent.loaded "args=[$a0][$a1] state=$st"; fi
la_print > "$out/launchctl-print.txt"

# menubar.screenshot (information)
sleep $(( t_install + 5 - $(date +%s) > 0 ? t_install + 5 - $(date +%s) : 0 ))
screencapture -x "$out/menubar-5s.png" 2>/dev/null

# sync.arrives
synced_now() { device=$(cat "$data/device-id" 2>/dev/null) && sync_ok "$device" tray; }
if wait_for 90 synced_now; then pass sync.arrives "device $device (sign-in from the keychain)"
else fail sync.arrives "device=$device stats=$(stats | jget - devices "$device")"; fi

sleep $(( t_install + 30 - $(date +%s) > 0 ? t_install + 30 - $(date +%s) : 0 ))
screencapture -x "$out/menubar-30s.png" 2>/dev/null
info menubar.screenshot "menubar-5s.png and menubar-30s.png in the install-$RUNNER_LABEL artifact"

# status.json
"$exe" status --json > "$out/status.json" 2>&1
c=$(jget "$out/status.json" snapshot client_mode); m=$(jget "$out/status.json" snapshot mode)
if [ "$c" = tray ] && [ "$m" = coordinated ]; then pass status.json "$m $c"; else fail status.json "mode=$m client_mode=$c"; fi

# run.no_listen
pid=$(la_pid)
l=$(lsof -nP -a -p "$pid" -iTCP -sTCP:LISTEN 2>/dev/null); u=$(lsof -nP -a -p "$pid" -iUDP 2>/dev/null)
if [ -n "$pid" ] && [ -z "$l" ] && [ -z "$u" ]; then pass run.no_listen "pid $pid"; else fail run.no_listen "pid=$pid tcp=$l udp=$u"; fi

# fakeserver.token_leaks
leaks=$(stats | jget - token_leaks)
if [ "$leaks" = 0 ]; then pass fakeserver.token_leaks 0; else fail fakeserver.token_leaks "$leaks"; fi

# upgrade.in_place
before=$(dev "$device" syncs)
oneliner > "$out/install-upgrade.log" 2>&1
rc=$?
newer() { [ "$(dev "$device" syncs)" -gt "$before" ] 2>/dev/null; }
wait_for 60 newer
pid2=$(la_pid); n=$(agent_count); device2=$(cat "$data/device-id" 2>/dev/null)
if [ $rc -eq 0 ] && [ -n "$pid2" ] && [ "$pid2" != "$pid" ] && [ "$n" -eq 1 ] && [ "$device2" = "$device" ] && newer; then
  pass upgrade.in_place "pid $pid -> $pid2"
else
  fail upgrade.in_place "rc=$rc pid $pid -> $pid2 processes=$n device_same=$([ "$device2" = "$device" ] && echo yes || echo no) syncs $before -> $(dev "$device" syncs)"
fi

# uninstall
"$exe" uninstall > "$out/uninstall.log" 2>&1
rc=$?
sleep 3
n=$(agent_count)
if [ $rc -eq 0 ] && [ ! -e "$plist" ] && ! launchctl print "gui/$(id -u)/$label" > /dev/null 2>&1 && [ "$n" -eq 0 ] && [ ! -e "$data" ]; then
  pass uninstall "rc=0"
else
  fail uninstall "rc=$rc plist=$([ -e "$plist" ] && echo left) loaded=$(launchctl print "gui/$(id -u)/$label" > /dev/null 2>&1 && echo yes) processes=$n data=$([ -e "$data" ] && echo left)"
fi
finish
