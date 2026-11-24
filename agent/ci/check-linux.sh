#!/usr/bin/env bash
# Linux runner checks: one-line install (headless, systemd user service),
# sync, status, no listening port, no token leak, in-place upgrade,
# uninstall, a desktop session without a tray host (falls back to
# headless with XDG autostart), one with a fake tray host (the
# top-bar label over D-Bus, the menu, what the helper process shows of
# itself), the menu rebuilt for a fresh install with nobody signed in, and
# the session lifecycle: the bus restarted at logout, a login with a
# second run, a late tray host and the agent's log file, two tray helpers
# and a killed agent leaving none behind, a failing helper coming back
# with the tray host (lifecycle-linux.sh). Results as annotations; exit 1 if a required check
# failed.
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=lib.sh
. "$here/lib.sh"

exe="$HOME/.local/bin/ai-usage"
data="$HOME/.config/ai-usage"
unit="$HOME/.config/systemd/user/ai-usage.service"
desktop="$HOME/.config/autostart/ai-usage.desktop"
oneliner() { timeout 180 sh -c "wget -qO- $BASE/files/install-ci.sh | sh"; }
agent_count() { pgrep -f "^$exe run" | wc -l; }
main_pid() { systemctl --user show -p MainPID --value ai-usage.service 2>/dev/null; }

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
a=$(systemctl --user is-active ai-usage.service 2>&1)
if [ "$a" = active ]; then pass systemd.active "$a"; else fail systemd.active "$a"; fi
systemctl --user status ai-usage.service --no-pager > "$out/systemd-status.txt" 2>&1

# sync.arrives
# run creates device-id when it starts, which may be just after install returns
synced_now() { device=$(cat "$data/device-id" 2>/dev/null) && sync_ok "$device" headless; }
if wait_for 90 synced_now; then pass sync.arrives "device $device"
else fail sync.arrives "device=$device stats=$(stats | jget - devices "$device")"; fi

# sync.schema2: the agent speaks schema 2 with a machine key (the fake
# server records only that one was there)
sc=$(dev "$device" last_schema); mk=$(dev "$device" machine)
if [ "$sc" = 2 ] && [ "$mk" = True ]; then pass sync.schema2 "schema $sc, machine key sent"
else fail sync.schema2 "schema=$sc machine=$mk"; fi

# status.json
"$exe" status --json > "$out/status.json" 2>&1
st="$out/status.json"
m=$(jget "$st" snapshot mode); r=$(jget "$st" snapshot providers claude role); c=$(jget "$st" snapshot client_mode)
k=$(jget "$st" snapshot providers claude rows 0 key); w=$(jget "$st" snapshot providers claude rows 0 percent)
if [ "$m" = coordinated ] && [ "$r" = standby ] && [ "$k" = weekly_all ] && [ "$w" = 42 ] && [ "$c" = headless ]; then pass status.json "$m $r $k $w $c"
else fail status.json "mode=$m role=$r row=$k weekly=$w client_mode=$c"; fi

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
ev=$(dev "$device" uninstall_events)
if [ $rc -eq 0 ] && [ ! -e "$unit" ] && [ ! -e "$exe" ] && [ ! -e "$data" ] && [ "$n" -eq 0 ] && [ "$ev" = 1 ]; then pass uninstall "rc=0, server told once"
else fail uninstall "rc=$rc unit=$([ -e "$unit" ] && echo left) exe=$([ -e "$exe" ] && echo left) data=$([ -e "$data" ] && echo left) processes=$n uninstall_events=$ev"; fi

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
# menu_check: the item's menu over D-Bus has the Claude header with its
# picture and a text bar row for the weekly limit (design D4, 6.5), and
# answers at once.
menu_check() {
  local layout got
  layout=$(timeout 15 busctl --address="$bus_addr" --timeout=10 --json=short -- \
    call "$svc" /StatusNotifierItem/menu com.canonical.dbusmenu GetLayout iias 0 -1 0 2>&1)
  printf '%s\n' "$layout" > "$out/menu-layout.json"
  got=$(printf '%s' "$layout" | python3 -c '
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    print("no_layout"); sys.exit()
items = []
def walk(n):
    i, props, kids = n
    p = {k: v["data"] for k, v in props.items()}
    if p.get("visible", True) and p.get("label"):
        items.append((p["label"], len(p.get("icon-data", []))))
    for k in kids:
        walk(k["data"])
walk(d["data"][1])
head = [l for l, icon in items if l.startswith("Claude") and icon > 0]
rows = [l for l, _ in items if "\u2588" in l and "Weekly" in l and "42%" in l]
print("ok" if head and rows else "missing: " + " | ".join(l for l, _ in items))
')
  if [ "$got" = ok ]; then pass tray.menu "Claude header with its picture, weekly bar row"
  else fail tray.menu "$got"; fi
}

# tray.helper_private: the helper's command line names nothing but the
# subcommand (its provider and icon folder come over its standard input),
# and it gets the desktop session's variables but not the rest of the
# agent's environment (a canary the agent has).
helper_private_check() {
  local hp ap cmd names problems=""
  hp=$(pgrep -f "^$exe tray-helper" | head -n 1)
  ap=$(pgrep -f "^$exe run" | head -n 1)
  if [ -z "$hp" ] || [ -z "$ap" ]; then fail tray.helper_private "helper=[$hp] agent=[$ap]"; return; fi
  cmd=$(tr '\0' ' ' < "/proc/$hp/cmdline")
  names=$(tr '\0' '\n' < "/proc/$hp/environ" | cut -d= -f1 | sort | tr '\n' ' ')
  [ "$cmd" = "$exe tray-helper " ] || problems="$problems cmdline=[$cmd]"
  tr '\0' '\n' < "/proc/$ap/environ" | grep -q '^CI_SECRET_CANARY=' || problems="$problems canary_not_in_agent"
  case " $names " in *" CI_SECRET_CANARY "*) problems="$problems canary_in_helper" ;; esac
  for v in DBUS_SESSION_BUS_ADDRESS XDG_CURRENT_DESKTOP XDG_SESSION_TYPE HOME PATH AI_USAGE_HELPER_TOKEN; do
    case " $names " in *" $v "*) ;; *) problems="$problems no_$v" ;; esac
  done
  if [ -z "$problems" ]; then pass tray.helper_private "command line [$cmd], environment: $names"
  else fail tray.helper_private "$problems; environment: $names"; fi
}

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
    CI_SECRET_CANARY=not-for-the-helper XDG_SESSION_TYPE=x11 XDG_CURRENT_DESKTOP=GNOME DBUS_SESSION_BUS_ADDRESS="$bus_addr" oneliner > "$out/install-tray.log" 2>&1
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
      tray_mode() { [ "$(dev "$device4" last_mode)" = tray ]; }
      wait_for 30 tray_mode || problems="$problems mode=$(dev "$device4" last_mode)"
      # Claude only: exactly one icon, drawn by one helper of the agent
      n_items=$(awk '$1 == "registered" { s[$2] = 1 } END { n = 0; for (k in s) n++; print n }' "$out/watcher.log")
      n_helpers=$(pgrep -f "^$exe tray-helper" | wc -l)
      [ "$n_items" = 1 ] && [ "$n_helpers" -eq 1 ] || problems="$problems items=$n_items helpers=$n_helpers"
      # tray.menu: the menu as the tray host reads it (dbusmenu GetLayout)
      menu_check
      helper_private_check
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
    [ "$(pgrep -f "^$exe tray-helper" | wc -l)" -eq 0 ] || problems="$problems helpers_after_uninstall"
  else
    problems="watcher not ready: $(head -c 200 "$out/watcher.err")"
  fi
  kill "$wpid" 2>/dev/null
  kill "$bus_pid" 2>/dev/null
  if [ -z "$problems" ]; then pass tray.label "label 42% -> 57% (signal), guide [$guide], one item from one helper"; else fail tray.label "$problems"; fi
}
tray_label_check

# tray.menu_reset: a fresh install with nobody signed in whose first status
# comes after the 5 s the tray waits for it (the server takes the sync and
# does not answer): the neutral helper's menu is built without a section,
# then rebuilt (systray ResetMenu) when "Sign in to Claude Code" comes. The
# menu is read as a tray host reads it (dbusmenu GetLayout) before and
# after, and a click on the rebuilt menu reaches the agent.
layout_of() { # layout_of FILE: the visible items as "label" lines, "-" for separators; first line the revision
  python3 -c '
import json, sys
try:
    d = json.load(open(sys.argv[1]))
except Exception:
    print("no_layout"); sys.exit()
rev, root = d["data"][0], d["data"][1]
print(rev)
for k in root[2]:
    i, props, _ = k["data"]
    p = {n: v["data"] for n, v in props.items()}
    if not p.get("visible", True):
        continue
    print("-" if p.get("type") == "separator" else "%d %s" % (i, p.get("label", "")))
' "$1"
}
menu_reset_check() {
  local bus_addr="" bus_pid="" wpid="" svc="" problems="" r1 r2 got1 got2 rid device5 fresh0
  mkdir -p "$out/signin-aside"
  mv "$HOME/.claude.json" "$HOME/.claude/.credentials.json" "$out/signin-aside/" 2>/dev/null
  curl -fsS --max-time 10 -X POST "$BASE/hold?seconds=40" || problems="$problems hold_failed"
  : > "$out/bus-reset.addr"; : > "$out/bus-reset.pid"
  if ! dbus-daemon --session --fork --print-address=5 --print-pid=6 5>"$out/bus-reset.addr" 6>"$out/bus-reset.pid"; then
    fail tray.menu_reset "dbus-daemon did not start"; return
  fi
  rbus_ready() { bus_addr=$(head -n 1 "$out/bus-reset.addr") && [ -n "$bus_addr" ] && bus_pid=$(head -n 1 "$out/bus-reset.pid") && [ -n "$bus_pid" ]; }
  wait_for 10 rbus_ready || { fail tray.menu_reset "no bus address"; return; }
  DBUS_SESSION_BUS_ADDRESS="$bus_addr" "$CI_BIN/fakewatcher" watch > "$out/watcher-reset.log" 2> "$out/watcher-reset.err" &
  wpid=$!
  rwatcher_ready() { grep -q '^ready$' "$out/watcher-reset.log"; }
  getlayout() {
    timeout 15 busctl --address="$bus_addr" --timeout=10 --json=short -- \
      call "$svc" /StatusNotifierItem/menu com.canonical.dbusmenu GetLayout iias 0 -1 0 > "$1" 2>&1
  }
  if wait_for 10 rwatcher_ready; then
    XDG_SESSION_TYPE=x11 XDG_CURRENT_DESKTOP=GNOME DBUS_SESSION_BUS_ADDRESS="$bus_addr" oneliner > "$out/install-reset.log" 2>&1 || problems="$problems install_rc=$?"
    rregistered() { svc=$(awk '$1 == "registered" { s = $2 } END { print s }' "$out/watcher-reset.log") && [ -n "$svc" ]; }
    if wait_for 30 rregistered; then
      getlayout "$out/menu-reset-1.json"
      layout_of "$out/menu-reset-1.json" > "$out/menu-reset-1.txt"
      r1=$(head -n 1 "$out/menu-reset-1.txt")
      got1=$(tail -n +2 "$out/menu-reset-1.txt" | sed 's/^[0-9]* //' | tr '\n' '|')
      # before the first status: the server line and the actions, no section
      case "$got1" in "Connecting to the server…|-|Open dashboard|Refresh now|Start at login|Remove AI Usage|Quit|") ;;
        *) problems="$problems first_menu=[$got1]" ;; esac
      signin() { getlayout "$out/menu-reset-2.json" && layout_of "$out/menu-reset-2.json" > "$out/menu-reset-2.txt" && grep -q ' Sign in to Claude Code$' "$out/menu-reset-2.txt"; }
      if wait_for 60 signin; then
        r2=$(head -n 1 "$out/menu-reset-2.txt")
        got2=$(tail -n +2 "$out/menu-reset-2.txt" | sed 's/^[0-9]* //' | tr '\n' '|')
        # rebuilt: the prompt in its section, a separator, the server line,
        # a separator, each action once
        case "$got2" in "Sign in to Claude Code|-|"*"|-|Open dashboard|Refresh now|Start at login|Remove AI Usage|Quit|") ;;
          *) problems="$problems rebuilt_menu=[$got2]" ;; esac
        [ "$r2" -gt "$r1" ] 2>/dev/null || problems="$problems revision=$r1->$r2"
        # a click on the rebuilt menu reaches the agent: Refresh now asks
        # the server (answering again) for fresh numbers at once
        curl -fsS --max-time 10 -X POST "$BASE/hold?seconds=0" || problems="$problems release_failed"
        device5=$(cat "$data/device-id" 2>/dev/null)
        fresh0=$(dev "$device5" want_fresh)
        rid=$(awk '$2 == "Refresh" && $3 == "now" { print $1 }' "$out/menu-reset-2.txt")
        if [ -n "$rid" ]; then
          busctl --address="$bus_addr" --timeout=10 -- call "$svc" /StatusNotifierItem/menu com.canonical.dbusmenu \
            Event isvu "$rid" clicked s "" 0 > "$out/menu-reset-click.txt" 2>&1 || problems="$problems click_failed"
          fresh() { [ "$(dev "$device5" want_fresh)" -gt "${fresh0:-0}" ] 2>/dev/null; }
          wait_for 90 fresh || problems="$problems click_not_heard(want_fresh=$(dev "$device5" want_fresh))"
        else
          problems="$problems no_refresh_item"
        fi
      else
        problems="$problems no_signin_prompt=[$(tail -n +2 "$out/menu-reset-2.txt" 2>/dev/null | tr '\n' '|')]"
      fi
    else
      problems="$problems not_registered"
    fi
    "$exe" uninstall > "$out/uninstall-reset.log" 2>&1 || problems="$problems uninstall_rc=$?"
    sleep 2
    [ "$(agent_count)" -eq 0 ] || problems="$problems processes_after_uninstall=$(agent_count)"
  else
    problems="$problems watcher_not_ready"
  fi
  curl -fsS --max-time 10 -X POST "$BASE/hold?seconds=0" > /dev/null 2>&1
  mv "$out/signin-aside/.claude.json" "$HOME/" 2>/dev/null
  mv "$out/signin-aside/.credentials.json" "$HOME/.claude/" 2>/dev/null
  kill "$wpid" 2>/dev/null
  kill "$bus_pid" 2>/dev/null
  if [ -z "$problems" ]; then pass tray.menu_reset "revision $r1 -> $r2: [$got1] -> [$got2], a click on the rebuilt menu heard"
  else fail tray.menu_reset "$problems"; fi
}
menu_reset_check

# The session lifecycle across a logout and login (lifecycle-linux.sh):
# tray.bus_restart, tray.relogin, tray.late_host and agent.log.
# shellcheck source=lifecycle-linux.sh
. "$here/lifecycle-linux.sh"
start_agent() { (export "$@"; oneliner) >> "$out/install-lifecycle.log" 2>&1; }
stop_agents() {
  "$exe" uninstall >> "$out/uninstall-lifecycle.log" 2>&1
  none_left() { [ "$(agent_count)" -eq 0 ] && [ "$(pgrep -f "^$exe tray-helper" | wc -l)" -eq 0 ]; }
  wait_for 15 none_left
}
lifecycle_checks

info linger "$(loginctl show-user "$USER" -p Linger 2>&1)"
finish
