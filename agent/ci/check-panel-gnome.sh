#!/usr/bin/env bash
# The usage panel on a real GNOME Shell (drawn-panel design 8, plan Task
# 15): a headless gnome-shell of this runner's Ubuntu (42.9 on 22.04, 46
# on 24.04, 50 on 26.04) in a private session (dbus-run-session, private
# HOME and XDG folders), first with tools/fakepanel as the agent, then
# with the CI build installed by the one-line install against the fake
# server. The checks themselves are in panel-session.sh. Every verdict and
# its numbers are a line of out/panel-results.txt, which the workflow
# turns into notices (failures are errors at once); screenshots in out/.
# Env: CI_DIST (release files), CI_BIN (fakeserver, fakepanel, pixcheck,
# fakegdm), CI_SCRIPTS (agent/ci), RUNNER_LABEL.
#
# What it changes on the runner, and undoes on exit: GNOME Shell creates
# its screen lock only when a display manager answers on the system bus
# (org.gnome.DisplayManager); when none does, fakegdm answers, allowed by
# a policy file in /etc/dbus-1/system.d that is removed again. Nothing it
# started outlives it: the shells, the agent and its helpers, the buses
# (also when it is interrupted: the exit trap; review B-M6).
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=lib.sh
. "$here/lib.sh"

label=${RUNNER_LABEL:?}
: > "$out/panel-results.txt"
res() { printf '%s\n' "$*" >> "$out/panel-results.txt"; }

ver=$(gnome-shell --version 2>&1 | head -n 1)
major=$(printf '%s' "$ver" | sed -nE 's/^GNOME Shell ([0-9]+).*/\1/p')
case "$major" in
  42) form=gnome42 flags="--wayland --mode=ubuntu" ;;
  46) form=gnome46 flags="--wayland --mode=ubuntu" ;;
  50) form=gnome46 flags="--mode=ubuntu" ;; # 50 has no --wayland (stage 0, Z1)
  *) fail panel.shell "no supported GNOME Shell: [$ver]"; res "panel.shell FAIL: [$ver]"; finish; exit 1 ;;
esac
appind=$(ls /usr/share/gnome-shell/extensions 2>/dev/null | grep -i appindicator | head -n 1)
res "panel.shell info: $label $ver, form $form, flags [$flags], tray fallback extension ${appind:-none} ($(dpkg-query -W -f '${Package} ${Version}' gnome-shell-extension-appindicator gnome-shell-ubuntu-extensions 2>/dev/null | tr '\n' ';'))"

start_fakeserver || { fail fakeserver "did not start"; finish; exit 1; }

# the display manager the shell's screen lock asks for
policy=/etc/dbus-1/system.d/ai-usage-ci-fakegdm.conf
gdm_pid=""
bus_pid=""
cleanup() {
  [ -n "${phome:-}" ] && pkill -f "^$phome/.local/bin/ai-usage" 2>/dev/null
  pkill -f "gnome-shell --headless" 2>/dev/null
  pkill -f "^$CI_BIN/fakepanel " 2>/dev/null
  [ -n "$bus_pid" ] && kill "$bus_pid" 2>/dev/null
  [ -n "${work:-}" ] && pkill -f "dbus-daemon --session --nofork --address=unix:path=$work/run/bus" 2>/dev/null
  [ -n "$gdm_pid" ] && kill "$gdm_pid" 2>/dev/null
  if [ -e "$policy" ]; then
    sudo rm -f "$policy"
    gdbus call --system --dest org.freedesktop.DBus --object-path /org/freedesktop/DBus \
      --method org.freedesktop.DBus.ReloadConfig > /dev/null 2>&1
  fi
  pkill -f -- "-listen 127.0.0.1:18700" 2>/dev/null
  [ -n "${work:-}" ] && rm -rf "$work" 2>/dev/null
  true
}
trap cleanup EXIT
dm_version() {
  gdbus call --system --dest org.gnome.DisplayManager --object-path /org/gnome/DisplayManager/Manager \
    --method org.freedesktop.DBus.Properties.Get org.gnome.DisplayManager.Manager Version 2>&1
}
dm=$(dm_version)
case "$dm" in
  "(<'"*) dm_from="a display manager on the runner $dm" ;;
  *)
    sudo tee "$policy" > /dev/null <<EOF
<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-BUS Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <policy user="$(id -un)">
    <allow own="org.gnome.DisplayManager"/>
  </policy>
  <policy context="default">
    <allow send_destination="org.gnome.DisplayManager"/>
  </policy>
</busconfig>
EOF
    gdbus call --system --dest org.freedesktop.DBus --object-path /org/freedesktop/DBus \
      --method org.freedesktop.DBus.ReloadConfig > /dev/null 2>&1
    "$CI_BIN/fakegdm" > "$out/fakegdm.log" 2>&1 &
    gdm_pid=$!
    wait_for 10 grep -q '^owns ' "$out/fakegdm.log"
    dm=$(dm_version)
    dm_from="fakegdm $dm (none on the runner)"
    ;;
esac
res "panel.lock info: display manager for the shell's lock: $dm_from"

# GNOME 42 makes its screen lock only for a login session it finds: its
# own (XDG_SESSION_ID) or the user's graphical one, which a runner has not
# (it stops at "this._userProxy.Display is null"); the runner user's
# session, when there is one, is named
sid=""
if [ -n "$gdm_pid" ] && [ "$major" = 42 ]; then
  sid=$(loginctl list-sessions --no-legend 2>/dev/null | awk -v u="$(id -un)" '$3 == u { print $1; exit }')
  res "panel.lock info: GNOME 42 is given the login session [${sid:-none}] of $(id -un)"
fi

# private folders; the runtime folder holds the Wayland socket, whose path
# must stay short
work=$(mktemp -d "$RUNNER_TEMP/gp.XXXXXX")
phome="$work/h"
mkdir -p "$phome" "$work/config" "$work/cache" "$work/data" "$work/run"
chmod 700 "$work/run"
# phase 1: one session; phase 2: the next one (same folders, a new bus)
# after the bus was away 35 s or more; phase 3: the one after that, whose
# bus is up without a shell for 35 s first (review B-I3). Phase 1's bus is
# dbus-run-session's; the later ones are at the runtime folder's address
# ($XDG_RUNTIME_DIR/bus, where systemd puts the user's bus), where the
# agent of phase 1, whose own address died with its bus, finds them
# (sessionbus.Address), as at a next login on a real PC.
bus_addr=""
# the private session's environment (the later buses are started in it
# too: the services they start on demand, such as dconf, see these folders)
senv() {
  env -i HOME="$phome" USER="$(id -un)" LANG=C.UTF-8 PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin \
    XDG_CONFIG_HOME="$work/config" XDG_DATA_HOME="$work/data" XDG_RUNTIME_DIR="$work/run" XDG_CACHE_HOME="$work/cache" \
    XDG_DATA_DIRS=/usr/share/ubuntu:/usr/share/gnome:/usr/local/share:/usr/share \
    XDG_CURRENT_DESKTOP=ubuntu:GNOME XDG_SESSION_TYPE=wayland ${sid:+XDG_SESSION_ID="$sid"} \
    ${bus_addr:+DBUS_SESSION_BUS_ADDRESS="$bus_addr"} \
    GITHUB_ACTIONS=true RUNNER_TEMP="$RUNNER_TEMP" GITHUB_WORKSPACE="${GITHUB_WORKSPACE:-$PWD}" "$@"
}
# session N [VAR=VALUE...]: phase N in the private session
session() {
  local n=$1 run=(dbus-run-session --)
  shift
  [ -n "$bus_addr" ] && run=()
  senv CI_DIST="$CI_DIST" CI_BIN="$CI_BIN" CI_SCRIPTS="$CI_SCRIPTS" PANEL_PRIVATE=1 PHASE="$n" FAKEGDM_PID="$gdm_pid" \
    LABEL="$label" MAJOR="$major" FORM="$form" FLAGS="$flags" APPIND="$appind" SHELL_VERSION="$ver" "$@" \
    timeout 1800 "${run[@]}" bash "$here/panel-session.sh" > "$out/session-$n.log" 2>&1
}
# start_bus N: phase N's bus at the runtime folder's address (bus_addr,
# bus_pid, bus_up: when it answered)
start_bus() {
  bus_addr=""
  rm -f "$work/run/bus"
  senv dbus-daemon --session --nofork --address="unix:path=$work/run/bus" > "$out/bus-$1.log" 2>&1 &
  bus_pid=$!
  wait_for 10 test -S "$work/run/bus" || { echo "::error title=panel-gnome::phase $1's session bus did not start ($(head -c 300 "$out/bus-$1.log"))"; return 1; }
  bus_up=$(date +%s.%N)
  bus_addr="unix:path=$work/run/bus"
}
# phase_end N: phase N ended its bus itself (what the bridge sees of a
# session's end); its verdicts are in phaseN.status
phase_end() {
  grep -E '^::(error|notice)' "$out/session-$1.log"
  [ -n "$bus_pid" ] && kill "$bus_pid" 2>/dev/null
  bus_pid=""
  [ -e "$out/phase$1.done" ]
}
rc1=1 rc2=1 rc3=1
session 1
rc1=$(cat "$out/phase1.status" 2>/dev/null || echo 1)
if phase_end 1; then
  # the bus stays away 35 s or more: a bridge stopped by quick failures
  # in that time (five exits with 75 in about 16 s) would show
  gone=$(cat "$out/bus-1.gone")
  sleep "$(python3 -c "import sys,time; print(max(0.0, 35.5 - (time.time() - float(sys.argv[1]))))" "$gone")"
  if start_bus 2; then
    away=$(python3 -c "import sys; print('%.1f' % (float(sys.argv[2]) - float(sys.argv[1])))" "$gone" "$bus_up")
    session 2 BUS_AWAY="$away"
    rc2=$(cat "$out/phase2.status" 2>/dev/null || echo 1)
    if phase_end 2 && start_bus 3; then
      session 3 BUS_UP="$bus_up"
      rc3=$?
      phase_end 3
    fi
  fi
fi
# nothing of the sessions may outlive them: the agent, its helpers, the
# shell (the exit trap does it again)
pkill -f "^$phome/.local/bin/ai-usage" 2>/dev/null
pkill -f "gnome-shell --headless" 2>/dev/null
rc=0
[ "$rc1" = 0 ] || { echo "::error title=panel-gnome::phase 1 failed (out/session-1.log)"; rc=1; }
[ "$rc2" = 0 ] || { echo "::error title=panel-gnome::phase 2 failed (out/session-2.log)"; rc=1; }
[ "$rc3" = 0 ] || { echo "::error title=panel-gnome::phase 3 failed (out/session-3.log)"; rc=1; }
exit "$rc"
