#!/usr/bin/env bash
# Linux runner checks: one-line install (headless, systemd user service),
# sync, status, no listening port, no token leak, in-place upgrade,
# uninstall, a desktop session without a tray host (falls back to
# headless with XDG autostart), and one with a fake tray host (the
# top-bar label over D-Bus). Results as annotations; exit 1 if a
# required check failed.
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=lib.sh
. "$here/lib.sh"

exe="$HOME/.local/bin/claude-usage"
data="$HOME/.config/claude-usage"
unit="$HOME/.config/systemd/user/claude-usage.service"
desktop="$HOME/.config/autostart/claude-usage.desktop"
oneliner() { timeout 180 sh -c "wget -qO- $BASE/files/install-ci.sh | sh"; }
agent_count() { pgrep -f "^$exe run" | wc -l; }
main_pid() { systemctl --user show -p MainPID --value claude-usage.service 2>/dev/null; }

fake_login file
start_fakeserver || { fail fakeserver "did not start"; finish; exit 1; }

# install.oneline
oneliner > "$out/install.log" 2>&1
rc=$?
if [ $rc -eq 0 ] && [ -x "$exe" ]; then pass install.oneline "rc=0"; else fail install.oneline "rc=$rc; $(tail -n 3 "$out/install.log")"; fi

# binary.static
f=$(file -b "$exe" 2>&1)
case "$f" in *"statically linked"*) pass binary.static "$f" ;; *) fail binary.static "$f" ;; esac

# systemd.active
a=$(systemctl --user is-active claude-usage.service 2>&1)
if [ "$a" = active ]; then pass systemd.active "$a"; else fail systemd.active "$a"; fi
systemctl --user status claude-usage.service --no-pager > "$out/systemd-status.txt" 2>&1

# sync.arrives
# run creates device-id when it starts, which may be just after install returns
synced_now() { device=$(cat "$data/device-id" 2>/dev/null) && sync_ok "$device" headless; }
if wait_for 90 synced_now; then pass sync.arrives "device $device"
else fail sync.arrives "device=$device stats=$(stats | jget - devices "$device")"; fi

# status.json
"$exe" status --json > "$out/status.json" 2>&1
st="$out/status.json"
m=$(jget "$st" snapshot mode); r=$(jget "$st" snapshot role); w=$(jget "$st" snapshot weekly_percent); c=$(jget "$st" snapshot client_mode)
if [ "$m" = coordinated ] && [ "$r" = standby ] && [ "$w" = 42 ] && [ "$c" = headless ]; then pass status.json "$m $r $w $c"
else fail status.json "mode=$m role=$r weekly=$w client_mode=$c"; fi

# run.no_listen
pid=$(main_pid)
l=$(ss -H -lntup 2>/dev/null | grep "pid=$pid," || true)
if [ -n "$pid" ] && [ "$pid" != 0 ] && [ -z "$l" ]; then pass run.no_listen "pid $pid"; else fail run.no_listen "pid=$pid $l"; fi

# fakeserver.token_leaks
leaks=$(stats | jget - token_leaks)
if [ "$leaks" = 0 ]; then pass fakeserver.token_leaks 0; else fail fakeserver.token_leaks "$leaks"; fi

# upgrade.in_place
before=$(dev "$device" syncs)
oneliner > "$out/install-upgrade.log" 2>&1
rc=$?
newer() { [ "$(dev "$device" syncs)" -gt "$before" ] 2>/dev/null; }
wait_for 60 newer
pid2=$(main_pid); n=$(agent_count); device2=$(cat "$data/device-id" 2>/dev/null)
if [ $rc -eq 0 ] && [ -n "$pid2" ] && [ "$pid2" != 0 ] && [ "$pid2" != "$pid" ] && [ "$n" -eq 1 ] && [ "$device2" = "$device" ] && newer; then
  pass upgrade.in_place "pid $pid -> $pid2"
else
  fail upgrade.in_place "rc=$rc pid $pid -> $pid2 processes=$n device_same=$([ "$device2" = "$device" ] && echo yes || echo no) syncs $before -> $(dev "$device" syncs)"
fi

# uninstall
"$exe" uninstall > "$out/uninstall.log" 2>&1
rc=$?
sleep 2
n=$(agent_count)
if [ $rc -eq 0 ] && [ ! -e "$unit" ] && [ ! -e "$exe" ] && [ ! -e "$data" ] && [ "$n" -eq 0 ]; then pass uninstall "rc=0"
else fail uninstall "rc=$rc unit=$([ -e "$unit" ] && echo left) exe=$([ -e "$exe" ] && echo left) data=$([ -e "$data" ] && echo left) processes=$n"; fi

# desktop.fallback_headless: a desktop session whose bus is gone
XDG_SESSION_TYPE=x11 XDG_CURRENT_DESKTOP=GNOME DBUS_SESSION_BUS_ADDRESS=unix:path=/nonexistent oneliner > "$out/install-desktop.log" 2>&1
rc=$?
exec_line=$(grep '^Exec=' "$desktop" 2>/dev/null)
headless_now() { device3=$(cat "$data/device-id" 2>/dev/null) && [ "$(dev "$device3" last_mode)" = headless ]; }
wait_for 90 headless_now
n=$(agent_count)
problems=""
[ $rc -eq 0 ] || problems="$problems rc=$rc"
[ "$exec_line" = "Exec=$exe run" ] || problems="$problems exec=[$exec_line]"
[ ! -e "$unit" ] || problems="$problems unit_left"
[ "$n" -eq 1 ] || problems="$problems processes=$n"
headless_now || problems="$problems mode=$(dev "$device3" last_mode)"
[ ! -e "$data/notify-pending" ] || problems="$problems notify_pending_left"
"$exe" uninstall > "$out/uninstall-desktop.log" 2>&1 || problems="$problems uninstall_rc=$?"
sleep 2
[ ! -e "$desktop" ] || problems="$problems desktop_file_left"
[ "$(agent_count)" -eq 0 ] || problems="$problems processes_after_uninstall=$(agent_count)"
if [ -z "$problems" ]; then pass desktop.fallback_headless "xdg-autostart, headless"; else fail desktop.fallback_headless "$problems"; fi

# tray.label: a desktop session with a tray host (a fake StatusNotifierWatcher
# on a private session bus). The item must export XAyatanaLabel (the text
# Ubuntu's AppIndicator extension shows next to the icon) and send
# XAyatanaNewLabel when the percentage changes.
tray_label_check() {
  local bus_addr="" bus_pid="" wpid="" svc="" label="" guide="" label2="" rc problems=""
  : > "$out/bus.addr"; : > "$out/bus.pid"
  if ! dbus-daemon --session --fork --print-address=5 --print-pid=6 5>"$out/bus.addr" 6>"$out/bus.pid"; then
    fail tray.label "dbus-daemon did not start"; return
  fi
  bus_ready() { bus_addr=$(head -n 1 "$out/bus.addr") && [ -n "$bus_addr" ] && bus_pid=$(head -n 1 "$out/bus.pid") && [ -n "$bus_pid" ]; }
  wait_for 10 bus_ready || { fail tray.label "no bus address"; return; }
  DBUS_SESSION_BUS_ADDRESS="$bus_addr" "$CI_BIN/fakewatcher" watch > "$out/watcher.log" 2> "$out/watcher.err" &
  wpid=$!
  watcher_ready() { grep -q '^ready$' "$out/watcher.log"; }
  if wait_for 10 watcher_ready; then
    XDG_SESSION_TYPE=x11 XDG_CURRENT_DESKTOP=GNOME DBUS_SESSION_BUS_ADDRESS="$bus_addr" oneliner > "$out/install-tray.log" 2>&1
    rc=$?
    [ $rc -eq 0 ] || problems="$problems rc=$rc"
    registered() { svc=$(awk '$1 == "registered" { s = $2 } END { print s }' "$out/watcher.log") && [ -n "$svc" ]; }
    get_prop() { DBUS_SESSION_BUS_ADDRESS="$bus_addr" "$CI_BIN/fakewatcher" get "$svc" "$1" 2>>"$out/watcher.err"; }
    label_is() { label=$(get_prop XAyatanaLabel) && [ "$label" = "$1" ]; }
    if wait_for 90 registered; then
      wait_for 30 label_is 42% || problems="$problems label=[$label]"
      guide=$(get_prop XAyatanaLabelGuide)
      [ -n "$guide" ] || problems="$problems guide_empty"
      device4=$(cat "$data/device-id" 2>/dev/null)
      [ "$(dev "$device4" last_mode)" = tray ] || problems="$problems mode=$(dev "$device4" last_mode)"
      curl -fsS --max-time 10 -X POST "$BASE/weekly?percent=57" || problems="$problems set_weekly_failed"
      signalled() { grep -q "^label $svc \"57%\" " "$out/watcher.log"; }
      wait_for 60 signalled || problems="$problems no_signal_57"
      label2=$(get_prop XAyatanaLabel)
      [ "$label2" = 57% ] || problems="$problems label_after=[$label2]"
      curl -fsS --max-time 10 -X POST "$BASE/weekly?percent=42" > /dev/null || true
    else
      problems="$problems not_registered"
    fi
    "$exe" uninstall > "$out/uninstall-tray.log" 2>&1 || problems="$problems uninstall_rc=$?"
    sleep 2
    [ "$(agent_count)" -eq 0 ] || problems="$problems processes_after_uninstall=$(agent_count)"
  else
    problems="watcher not ready: $(head -c 200 "$out/watcher.err")"
  fi
  kill "$wpid" 2>/dev/null
  kill "$bus_pid" 2>/dev/null
  if [ -z "$problems" ]; then pass tray.label "label 42% -> 57% (signal), guide [$guide]"; else fail tray.label "$problems"; fi
}
tray_label_check

info linger "$(loginctl show-user "$USER" -p Linger 2>&1)"
finish
