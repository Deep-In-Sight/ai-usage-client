#!/usr/bin/env bash
# macOS runner checks: the Claude sign-in only in the login keychain and a
# fake Codex sign-in in ~/.codex, one-line install, LaunchAgent, tray mode
# sync of both providers, one helper per provider, status, no listening
# port, no token leak, in-place upgrade and uninstall (the server told). Results as annotations; exit 1 if a
# required check failed.
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=lib.sh
. "$here/lib.sh"

label=io.github.deep-in-sight.ai-usage
plist="$HOME/Library/LaunchAgents/$label.plist"
data="$HOME/Library/Application Support/AIUsage"
exe="$data/ai-usage"
tmo() { perl -e 'alarm shift; exec @ARGV' "$@"; } # no timeout(1) on macOS
oneliner() { tmo 180 sh -c "curl -fsSL $BASE/files/install-ci.sh | sh"; }
agent_count() { pgrep -f "^$exe run" | wc -l | tr -d ' '; }
# the tray helpers (design 6.4) and those whose parent is a pid
helper_count() { pgrep -f "^$exe tray-helper" | wc -l | tr -d ' '; }
children() { local p n=0; for p in $(pgrep -f "^$exe tray-helper"); do [ "$(ps -o ppid= -p "$p" | tr -d ' ')" = "$1" ] && n=$((n + 1)); done; echo $n; }
la_print() { launchctl print "gui/$(id -u)/$label" 2>&1; }
la_pid() { la_print | awk -F'= ' '/^[[:space:]]*pid = /{ print $2; exit }'; }
la_state() { la_print | awk -F'= ' '/^[[:space:]]*state = /{ print $2; exit }'; }

fake_login # account only: the sign-in is in the keychain
rm -f "$HOME/.claude/.credentials.json"
security add-generic-password -U -a "$USER" -s "Claude Code-credentials" -w "$(fake_creds_json)" > /dev/null 2>&1 ||
  fail keychain.setup "could not add the keychain item"
trap 'security delete-generic-password -s "Claude Code-credentials" > /dev/null 2>&1' EXIT
fake_codex_login
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

# sync.codex: the Codex block is signed in, with the fake Codex account
if wait_for 30 codex_ok "$device"; then pass sync.codex "codex ok, $CODEX_EMAIL"
else fail sync.codex "providers=$(dev "$device" providers)"; fi

# status.both: the status command shows Claude's and Codex's parts
if wait_for 30 both_in_status "$exe"; then pass status.both "Claude and Codex (Plus): 5-hour 42%, Weekly 17%"
else fail status.both "$(head -c 300 "$out/status-both.txt")"; fi

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

# tray.provider_helpers: Claude and Codex signed in, one helper each, both
# children of the agent
provider_helpers() { [ "$(helper_count)" -eq 2 ] && [ "$(children "$pid")" -eq 2 ]; }
if wait_for 30 provider_helpers; then pass tray.provider_helpers "agent $pid, helpers $(pgrep -f "^$exe tray-helper" | tr '\n' ' ')"
else fail tray.provider_helpers "helpers=$(helper_count) children_of_$pid=$(children "$pid")"; fi

# tray.helper_cmdline: the helper's command line names nothing but the
# subcommand (its provider and icon folder come over its standard input)
hcmd=$(ps -ww -o command= -p "$(pgrep -f "^$exe tray-helper" | head -n 1)" 2>/dev/null)
if [ "$hcmd" = "$exe tray-helper" ]; then pass tray.helper_cmdline "[$hcmd]"; else fail tray.helper_cmdline "[$hcmd]"; fi

# tray.two_helpers: the CI build's test switch shows a second helper; the
# agent is started by hand for this (the LaunchAgent is stopped
# meanwhile), then the LaunchAgent is started again
launchctl bootout "gui/$(id -u)/$label" > /dev/null 2>&1
none() { [ "$(agent_count)" -eq 0 ] && [ "$(helper_count)" -eq 0 ]; }
wait_for 15 none
CI_SECRET_CANARY=not-for-the-helper AI_USAGE_TEST_FAKE_PROVIDERS=claude,codex "$exe" run > "$out/run-two-helpers.log" 2>&1 &
pid2h=$!
two() { [ "$(children "$pid2h")" -eq 2 ]; }
if wait_for 60 two; then
  sleep 5
  screencapture -x "$out/menubar-two-helpers.png" 2>/dev/null
  pass tray.two_helpers "agent $pid2h, helpers $(pgrep -f "^$exe tray-helper" | tr '\n' ' '); menubar-two-helpers.png in the artifact"
  # the helpers get an allow-list of the agent's environment: the canary
  # the agent has stays with it (when ps can show environments here)
  leaked=0
  for hp in $(pgrep -f "^$exe tray-helper"); do
    ps -E -ww -o command= -p "$hp" 2>/dev/null | grep -q 'CI_SECRET_CANARY=' && leaked=$((leaked + 1))
  done
  if ps -E -ww -o command= -p "$pid2h" 2>/dev/null | grep -q 'CI_SECRET_CANARY='; then
    if [ "$leaked" -eq 0 ]; then pass tray.helper_env "the agent's canary is not in its helpers' environment"
    else fail tray.helper_env "the canary is in $leaked helpers' environment"; fi
  else
    info tray.helper_env "ps shows no environment here (agent's canary not visible); not checked"
  fi
else fail tray.two_helpers "helpers=$(helper_count) children=$(children "$pid2h")"; fi
kill -9 "$pid2h" 2>/dev/null
nohelper() { [ "$(helper_count)" -eq 0 ]; }
if wait_for 6 nohelper; then pass tray.helpers_follow_main "no helper left"; else fail tray.helpers_follow_main "helpers=$(helper_count)"; fi
launchctl bootstrap "gui/$(id -u)" "$plist" > /dev/null 2>&1
wait_for 20 running
pid=$(la_pid)

# fakeserver.token_leaks (the Claude token and both Codex tokens)
leaks=$(stats | jget - token_leaks)
if [ "$leaks" = 0 ]; then pass fakeserver.token_leaks 0; else fail fakeserver.token_leaks "$leaks"; fi

# files.token_leaks: no fake token in the agent's files
if h=$(no_fake_tokens "$data"); then pass files.token_leaks "none"; else fail files.token_leaks "$h"; fi

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
n=$(( $(agent_count) + $(helper_count) ))
ev=$(dev "$device" uninstall_events)
if [ $rc -eq 0 ] && [ ! -e "$plist" ] && ! launchctl print "gui/$(id -u)/$label" > /dev/null 2>&1 && [ "$n" -eq 0 ] && [ ! -e "$data" ] && [ "$ev" = 1 ]; then
  pass uninstall "rc=0, server told once"
else
  fail uninstall "rc=$rc plist=$([ -e "$plist" ] && echo left) loaded=$(launchctl print "gui/$(id -u)/$label" > /dev/null 2>&1 && echo yes) processes=$n data=$([ -e "$data" ] && echo left) uninstall_events=$ev"
fi
finish
