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
# the usage panel's GNOME extension (drawn-panel design 6.6) and its
# staging folders: nothing writes them on these sessions without GNOME
# Shell, so the uninstall check plants a copy for removal to delete
gshell="${XDG_DATA_HOME:-$HOME/.local/share}/gnome-shell"
panel_dir="$gshell/extensions/ai-usage-panel@localhost"
oneliner() { timeout 180 sh -c "wget -qO- $BASE/files/install-ci.sh | sh"; }
agent_count() { pgrep -f "^$exe run" | wc -l; }
main_pid() { systemctl --user show -p MainPID --value ai-usage.service 2>/dev/null; }

fake_login file
fake_codex_login
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

# run.vendors_off: the CI build sends nothing to Anthropic or OpenAI
if vendors_off "$data"; then pass run.vendors_off "CI build: no request to Anthropic or OpenAI"
else fail run.vendors_off "no such line in agent.log"; fi

# sync.schema2: the agent speaks schema 2 with a machine key (the fake
# server records only that one was there)
sc=$(dev "$device" last_schema); mk=$(dev "$device" machine)
if [ "$sc" = 2 ] && [ "$mk" = True ]; then pass sync.schema2 "schema $sc, machine key sent"
else fail sync.schema2 "schema=$sc machine=$mk"; fi

# sync.codex: the Codex block is signed in, with the fake Codex account
if wait_for 30 codex_ok "$device"; then pass sync.codex "codex ok, $CODEX_EMAIL"
else fail sync.codex "providers=$(dev "$device" providers)"; fi

# status.both: the status command shows Claude's and Codex's parts, the
# Codex rows from the server's latest (5-hour 42 %, weekly 17 %)
if wait_for 30 both_in_status "$exe"; then pass status.both "Claude and Codex (Plus): 5-hour 42%, Weekly 17%"
else fail status.both "$(head -c 300 "$out/status-both.txt")"; fi

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

# fakeserver.token_leaks (the Claude token and both Codex tokens)
leaks=$(stats | jget - token_leaks)
if [ "$leaks" = 0 ]; then pass fakeserver.token_leaks 0; else fail fakeserver.token_leaks "$leaks"; fi

# files.token_leaks: no fake token in the agent's files (status.json,
# backlog.json, latest-*.json, state.json, agent.log)
if h=$(no_fake_tokens "$data"); then pass files.token_leaks "none in $(ls "$data" | tr '\n' ' ')"
else fail files.token_leaks "$h"; fi

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

# uninstall (also of a panel extension left from a GNOME session, with a
# staging folder of an interrupted write)
mkdir -p "$panel_dir" "$gshell/.ai-usage-panel.new-4242"
printf '{"uuid": "ai-usage-panel@localhost"}\n' > "$panel_dir/metadata.json"
"$exe" uninstall > "$out/uninstall.log" 2>&1
rc=$?
sleep 2
n=$(agent_count)
ev=$(dev "$device" uninstall_events)
if [ $rc -eq 0 ] && [ ! -e "$unit" ] && [ ! -e "$exe" ] && [ ! -e "$data" ] && [ ! -e "$panel_dir" ] && [ ! -e "$gshell/.ai-usage-panel.new-4242" ] && [ "$n" -eq 0 ] && [ "$ev" = 1 ]; then pass uninstall "rc=0, server told once, panel extension removed"
else fail uninstall "rc=$rc unit=$([ -e "$unit" ] && echo left) exe=$([ -e "$exe" ] && echo left) data=$([ -e "$data" ] && echo left) panel=$([ -e "$panel_dir" ] && echo left) staging=$([ -e "$gshell/.ai-usage-panel.new-4242" ] && echo left) processes=$n uninstall_events=$ev"; fi

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
[ ! -e "$panel_dir" ] || problems="$problems panel_extension_left"
[ "$(agent_count)" -eq 0 ] || problems="$problems processes_after_uninstall=$(agent_count)"
if [ -z "$problems" ]; then pass desktop.fallback_headless "xdg-autostart, headless"; else fail desktop.fallback_headless "$problems"; fi

# The tray checks below and the lifecycle want Claude alone (one icon);
# tray.codex and sync.schema1 at the end put the Codex sign-in back.
codex_aside

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

# tray.helper_private: every helper's command line names nothing but the
# subcommand (its provider and icon folder, or its shell role, come over
# its standard input), and it gets the desktop session's variables but not
# the rest of the agent's environment (a canary the agent has): the icon
# helper's and the GNOME bridge's (BUS: the session bus it serves on). The
# message names each helper's own command line and variables (review m8).
helper_private_check() {
  local bus=$1 hp ap cmd names problems="" bridge role seen="" each=""
  ap=$(pgrep -f "^$exe run" | head -n 1)
  bridge=$(bridge_pids "$bus" | head -n 1)
  [ -n "$ap" ] || problems="$problems no_agent"
  # on these buses without GNOME Shell the bridge ends 30 s after it started
  [ -n "$bridge" ] || problems="$problems no_bridge(it lives 30 s without a GNOME Shell)"
  tr '\0' '\n' < "/proc/$ap/environ" 2>/dev/null | grep -q '^CI_SECRET_CANARY=' || problems="$problems canary_not_in_agent"
  for hp in $(pgrep -f "^$exe tray-helper"); do
    cmd=$(tr '\0' ' ' < "/proc/$hp/cmdline" 2>/dev/null)
    names=$(tr '\0' '\n' < "/proc/$hp/environ" 2>/dev/null | cut -d= -f1 | sort | tr '\n' ' ')
    if [ "$hp" = "$bridge" ]; then role=bridge; else role=icon; fi
    seen="$seen $role"
    each="$each; $role $hp: command line [$cmd], environment: $names"
    [ "$cmd" = "$exe tray-helper " ] || problems="$problems ${role}_${hp}_cmdline=[$cmd]"
    case " $names " in *" CI_SECRET_CANARY "*) problems="$problems canary_in_${role}_$hp" ;; esac
    for v in DBUS_SESSION_BUS_ADDRESS XDG_CURRENT_DESKTOP XDG_SESSION_TYPE HOME PATH AI_USAGE_HELPER_TOKEN; do
      case " $names " in *" $v "*) ;; *) problems="$problems no_${v}_${role}_$hp" ;; esac
    done
  done
  case "$seen" in *icon*) ;; *) problems="$problems no_icon_helper" ;; esac
  if [ -z "$problems" ]; then pass tray.helper_private "helpers [$seen ]$each"
  else fail tray.helper_private "$problems; helpers [$seen ]$each"; fi
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
    # tray.helper_private as soon as the bridge and an icon helper run, not
    # after the registration (up to 90 s): the bridge waits only 30 s for
    # a GNOME Shell on this bus (review m8)
    helpers_up() { [ -n "$(bridge_pids "$bus_addr")" ] && [ "$(icon_helper_count "$exe" "$bus_addr")" -ge 1 ]; }
    wait_for 20 helpers_up
    helper_private_check "$bus_addr"
    if wait_for 90 registered; then
      wait_for 30 label_is 42% || problems="$problems label=[$label]"
      guide=$(get_prop XAyatanaLabelGuide)
      [ -n "$guide" ] || problems="$problems guide_empty"
      device4=$(cat "$data/device-id" 2>/dev/null)
      tray_mode() { [ "$(dev "$device4" last_mode)" = tray ]; }
      wait_for 30 tray_mode || problems="$problems mode=$(dev "$device4" last_mode)"
      # Claude only: exactly one icon, drawn by one helper of the agent
      n_items=$(awk '$1 == "registered" { s[$2] = 1 } END { n = 0; for (k in s) n++; print n }' "$out/watcher.log")
      one_icon_helper() { n_helpers=$(icon_helper_count "$exe" "$bus_addr"); [ "$n_helpers" -eq 1 ]; }
      wait_for 10 one_icon_helper
      [ "$n_items" = 1 ] && [ "$n_helpers" -eq 1 ] || problems="$problems items=$n_items helpers=$n_helpers"
      # tray.menu: the menu as the tray host reads it (dbusmenu GetLayout)
      menu_check
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
# then rebuilt (systray ResetMenu) when "Sign in to Claude Code or Codex"
# comes. The
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
      signin() { getlayout "$out/menu-reset-2.json" && layout_of "$out/menu-reset-2.json" > "$out/menu-reset-2.txt" && grep -q ' Sign in to Claude Code or Codex$' "$out/menu-reset-2.txt"; }
      if wait_for 60 signin; then
        r2=$(head -n 1 "$out/menu-reset-2.txt")
        got2=$(tail -n +2 "$out/menu-reset-2.txt" | sed 's/^[0-9]* //' | tr '\n' '|')
        # rebuilt: the prompt in its section, a separator, the server line,
        # a separator, each action once
        case "$got2" in "Sign in to Claude Code or Codex|-|"*"|-|Open dashboard|Refresh now|Start at login|Remove AI Usage|Quit|") ;;
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
codex_back

# fake_session NAME: a private session bus with the fake tray host; sets
# bus_addr, bus_pid, wpid (false if it did not come up).
fake_session() {
  bus_addr=""; bus_pid=""; wpid=""
  : > "$out/bus-$1.addr"; : > "$out/bus-$1.pid"
  dbus-daemon --session --fork --print-address=5 --print-pid=6 5>"$out/bus-$1.addr" 6>"$out/bus-$1.pid" || return 1
  sbus_ready() { bus_addr=$(head -n 1 "$out/bus-$1.addr") && [ -n "$bus_addr" ] && bus_pid=$(head -n 1 "$out/bus-$1.pid") && [ -n "$bus_pid" ]; }
  wait_for 10 sbus_ready "$1" || return 1
  DBUS_SESSION_BUS_ADDRESS="$bus_addr" "$CI_BIN/fakewatcher" watch > "$out/watcher-$1.log" 2> "$out/watcher-$1.err" &
  wpid=$!
  swatcher_ready() { grep -q '^ready$' "$out/watcher-$1.log"; }
  wait_for 10 swatcher_ready "$1"
}
end_session() { kill "$wpid" 2>/dev/null; kill "$bus_pid" 2>/dev/null; }

# tray.codex: Claude and Codex signed in on a desktop: two items from two
# helpers, labelled with each provider's number (Claude weekly 42 %, Codex
# weekly 17 %: the server's latest), and Codex's section in the menu as a
# tray host reads it (header with the plan, the 17 % weekly row). A CI
# build sends nothing to Anthropic or OpenAI.
tray_codex_check() {
  local problems="" items labels svc l layout got
  # Claude's weekly number as the server answers it (earlier checks change it)
  curl -fsS --max-time 10 -X POST "$BASE/weekly?percent=42" || problems="$problems set_weekly_failed"
  fake_session codex || { fail tray.codex "no session bus or tray host"; end_session; return; }
  XDG_SESSION_TYPE=x11 XDG_CURRENT_DESKTOP=GNOME DBUS_SESSION_BUS_ADDRESS="$bus_addr" \
    AI_USAGE_TEST_DUMP_VIEW="$out/view-codex.json" oneliner > "$out/install-codex.log" 2>&1 || problems="$problems install_rc=$?"
  two_items() { items=$(awk '$1 == "registered" { s[$2] = 1 } END { for (k in s) print k }' "$out/watcher-codex.log" | sort) && [ "$(printf '%s\n' "$items" | grep -c .)" -eq 2 ]; }
  both_labels() {
    labels=""
    for svc in $items; do
      l=$(DBUS_SESSION_BUS_ADDRESS="$bus_addr" "$CI_BIN/fakewatcher" get "$svc" XAyatanaLabel 2>>"$out/watcher-codex.err")
      labels="$labels $l"
    done
    labels=$(printf '%s\n' $labels | sort | tr '\n' ' ')
    [ "$labels" = "17% 42% " ]
  }
  if wait_for 90 two_items; then
    wait_for 60 both_labels || problems="$problems labels=[$labels]"
    two_icon_helpers() { [ "$(icon_helper_count "$exe" "$bus_addr")" -eq 2 ]; }
    wait_for 10 two_icon_helpers || problems="$problems helpers=$(icon_helper_count "$exe" "$bus_addr")"
    svc=$(printf '%s\n' "$items" | head -n 1)
    layout=$(timeout 15 busctl --address="$bus_addr" --timeout=10 --json=short -- \
      call "$svc" /StatusNotifierItem/menu com.canonical.dbusmenu GetLayout iias 0 -1 0 2>&1)
    printf '%s\n' "$layout" > "$out/menu-codex.json"
    got=$(printf '%s' "$layout" | python3 -c '
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    print("no_layout"); sys.exit()
labels = []
def walk(n):
    i, props, kids = n
    p = {k: v["data"] for k, v in props.items()}
    if p.get("visible", True) and p.get("label"):
        labels.append(p["label"])
    for k in kids:
        walk(k["data"])
walk(d["data"][1])
head = [l for l in labels if l.startswith("Codex \u00b7 user-ci@") and l.endswith("Plus")]
rows = [l for l in labels if "17%" in l and "Weekly" in l]
print("ok" if head and rows else "missing: " + " | ".join(labels))
')
    [ "$got" = ok ] || problems="$problems codex_menu=[$got]"
    grep -q 'Codex · user-ci' "$out/view-codex.json" 2>/dev/null || problems="$problems no_view_dump"
  else
    problems="$problems items=[$(printf '%s' "$items" | tr '\n' ' ')]"
  fi
  "$exe" uninstall > "$out/uninstall-codex.log" 2>&1 || problems="$problems uninstall_rc=$?"
  sleep 2
  [ "$(agent_count)" -eq 0 ] && [ "$(pgrep -f "^$exe tray-helper" | wc -l)" -eq 0 ] || problems="$problems processes_after_uninstall"
  end_session
  if [ -z "$problems" ]; then pass tray.codex "two items, labels [$labels], Codex section in the menu"; else fail tray.codex "$problems"; fi
}
tray_codex_check

# sync.schema1: a server from before schema 2 (-schema1-only): the agent
# falls back to schema 1 and keeps syncing (Claude), and the menu it shows
# (dumped by the CI build) says the server does not support Codex yet.
schema1_check() {
  local problems="" base1=http://127.0.0.1:18701 dev1 sc n
  start_fakeserver 18701 -schema1-only || { fail sync.schema1 "the schema-1 fake server did not start"; return; }
  fake_session schema1 || { fail sync.schema1 "no session bus or tray host"; end_session; return; }
  XDG_SESSION_TYPE=x11 XDG_CURRENT_DESKTOP=GNOME DBUS_SESSION_BUS_ADDRESS="$bus_addr" \
    AI_USAGE_TEST_DUMP_VIEW="$out/view-schema1.json" timeout 180 sh -c "wget -qO- $base1/files/install-ci.sh | sh" \
    > "$out/install-schema1.log" 2>&1 || problems="$problems install_rc=$?"
  s1stats() { curl -fsS --max-time 10 "$base1/stats"; }
  synced1() { dev1=$(cat "$data/device-id" 2>/dev/null) && [ "$(s1stats | jget - devices "$dev1" syncs)" -ge 2 ] 2>/dev/null; }
  wait_for 120 synced1 || problems="$problems syncs=$(s1stats | jget - devices "$dev1" syncs)"
  sc=$(s1stats | jget - devices "$dev1" last_schema)
  [ "$sc" = 1 ] || problems="$problems last_schema=$sc"
  said() { grep -q 'Server does not support Codex yet' "$out/view-schema1.json" 2>/dev/null; }
  wait_for 60 said || problems="$problems view=[$(head -c 200 "$out/view-schema1.json" 2>/dev/null)]"
  n=$(s1stats | jget - token_leaks)
  [ "$n" = 0 ] || problems="$problems token_leaks=$n"
  "$exe" uninstall > "$out/uninstall-schema1.log" 2>&1 || problems="$problems uninstall_rc=$?"
  sleep 2
  end_session
  n=$(s1stats | jget - devices "$dev1" syncs)
  pkill -f -- "-listen 127.0.0.1:18701" 2>/dev/null
  if [ -z "$problems" ]; then pass sync.schema1 "schema 1, $n syncs, menu: Server does not support Codex yet"
  else fail sync.schema1 "$problems"; fi
}
schema1_check

# release.no_codex_cli: a release-style build (CI_REL_VERSION, such as
# 0.8.99-relcheck: no
# CI-build switch, no refusing transport) never starts the Codex CLI
# (ruling F1). Every Anthropic and OpenAI host name is mapped to this
# runner first and answered by fakevendor, whose made-up CA the system
# trusts, so nothing can reach the real hosts; the check stops before the
# install if any name is not local. A fakecodex that records every run is
# first on PATH (install's and the service's), is the codex_path install
# records, and is named by the CI build's fallback switch. chatgpt.com
# answers 403 for three fetch rounds, then 200 with both limit fields
# null for three more: codex must never have run, and every Codex result
# the server got must be the direct request's own answer.
release_codex_check() {
  local problems="" relbin="$RUNNER_TEMP/relbin" ca="$out/fakevendor-ca.pem" marker="$out/codex-ran.txt"
  local base2=http://127.0.0.1:18702 admin=http://127.0.0.1:18799 dev2="" h code got results v
  local names="chatgpt.com chat.openai.com api.openai.com auth.openai.com api.anthropic.com console.anthropic.com claude.ai"
  if [ -z "${CI_REL:-}" ] || [ ! -f "$CI_REL/ai-usage-linux-amd64" ]; then fail release.no_codex_cli "no release-style build in CI_REL"; return; fi
  trap release_cleanup EXIT
  trap 'release_cleanup; exit 130' INT TERM
  # 1. the vendors' names are this runner's, before anything could ask them
  REL_HOSTS_SAVED="$RUNNER_TEMP/hosts.before-release-check"
  cp /etc/hosts "$REL_HOSTS_SAVED"
  # both families: a name mapped for IPv4 only is looked up in DNS for IPv6
  printf '127.0.0.1 %s # ai-usage CI fake vendors\n::1 %s # ai-usage CI fake vendors\n' "$names" "$names" | sudo tee -a /etc/hosts > /dev/null
  for h in $names; do
    got=$(getent ahosts "$h" | awk '{ print $1 }' | sort -u)
    # at least one address, and every one a loopback address
    if [ -z "$got" ] || ! printf '%s\n' "$got" | awk '$0 != "127.0.0.1" && $0 != "::1" { bad = 1 } END { exit bad }'; then
      problems="$problems $h=[$(printf '%s' "$got" | tr '\n' ' ')]"
    fi
  done
  if [ -n "$problems" ]; then fail release.no_codex_cli "vendor names not mapped to this runner:$problems"; release_cleanup; return; fi
  # 2. the fake vendors on 127.0.0.1:443, their CA trusted by the system
  REL_PORT_OLD=$(sysctl -n net.ipv4.ip_unprivileged_port_start)
  sudo sysctl -q -w net.ipv4.ip_unprivileged_port_start=443
  "$CI_BIN/fakevendor" -listen '127.0.0.1:443,[::1]:443' -admin 127.0.0.1:18799 -ca "$ca" -mode 403 > "$out/fakevendor.log" 2>&1 &
  REL_VPID=$!
  if ! wait_for 20 curl -fsS --max-time 5 "$admin/healthz" -o /dev/null; then
    fail release.no_codex_cli "fakevendor did not start: $(head -c 200 "$out/fakevendor.log")"; release_cleanup; return
  fi
  REL_CA=/usr/local/share/ca-certificates/ai-usage-ci-fakevendor.crt
  sudo cp "$ca" "$REL_CA" && sudo update-ca-certificates > "$out/update-ca.log" 2>&1
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 https://chatgpt.com/backend-api/wham/usage)
  if [ "$code" != 403 ]; then fail release.no_codex_cli "chatgpt.com is not the fake vendor (HTTP $code)"; release_cleanup; return; fi
  # 3. the fakecodex, first on PATH for install and for the service
  mkdir -p "$relbin"
  cp "$CI_BIN/fakecodex" "$relbin/codex"
  printf -- '-record\n%s\n' "$marker" > "$relbin/codex.args"
  REL_PATH_OLD=$(systemctl --user show-environment | sed -n 's/^PATH=//p')
  REL_SWITCH=1
  systemctl --user set-environment PATH="$relbin:${REL_PATH_OLD:-$PATH}" AI_USAGE_TEST_CODEX_FALLBACK="$relbin/codex"
  # 4. a fake server with the release-style files that asks for Codex at every sync
  start_fakeserver 18702 -codex-poller -files "$CI_REL" || { fail release.no_codex_cli "the second fake server did not start"; release_cleanup; return; }
  PATH="$relbin:$PATH" AI_USAGE_TEST_CODEX_FALLBACK="$relbin/codex" timeout 180 sh -c "wget -qO- $base2/files/install-ci.sh | sh" \
    > "$out/install-release.log" 2>&1 || problems="$problems install_rc=$?"
  s2() { curl -fsS --max-time 10 "$base2/stats"; }
  rounds() { dev2=$(cat "$data/device-id" 2>/dev/null) && [ "$(s2 | jget - devices "$dev2" codex_results "$1")" -ge "$2" ] 2>/dev/null; }
  wait_for 300 rounds "wham 403" 3 || problems="$problems rounds_403=[$(s2 | jget - devices "$dev2" codex_results)]"
  curl -fsS --max-time 10 -X POST "$admin/mode?m=nulls" || problems="$problems mode_failed"
  wait_for 300 rounds "wham 200" 3 || problems="$problems rounds_200=[$(s2 | jget - devices "$dev2" codex_results)]"
  # 5. what happened
  [ ! -e "$marker" ] || problems="$problems codex_was_run=[$(head -c 200 "$marker" | tr '\n' ' ')]"
  results=$(s2 | jget - devices "$dev2" codex_results)
  printf '%s' "$results" | python3 -c 'import json,sys; d=json.load(sys.stdin); sys.exit(0 if d and set(d) <= {"wham 403", "wham 200"} else 1)' ||
    problems="$problems results=[$results]"
  [ "$(jget "$data/config.json" codex_path)" = "$relbin/codex" ] || problems="$problems codex_path=[$(jget "$data/config.json" codex_path)]"
  v=$(s2 | jget - devices "$dev2" last_version)
  [ -n "$v" ] && [ "$v" = "${CI_REL_VERSION:-}" ] || problems="$problems version=$v want=${CI_REL_VERSION:-}"
  ! grep -q 'CI build' "$data/agent.log" 2>/dev/null || problems="$problems behaves_as_ci_build"
  [ "$(s2 | jget - token_leaks)" = 0 ] || problems="$problems token_leaks=$(s2 | jget - token_leaks)"
  curl -fsS --max-time 10 "$admin/stats" > "$out/fakevendor-stats.json" 2>&1
  cp "$data/agent.log" "$out/agent-release.log" 2>/dev/null
  "$exe" uninstall > "$out/uninstall-release.log" 2>&1 || problems="$problems uninstall_rc=$?"
  sleep 2
  [ "$(agent_count)" -eq 0 ] || problems="$problems processes_after_uninstall=$(agent_count)"
  # nothing the check started may outlive the fake vendors (re-review R-3)
  release_stop_agents || problems="$problems agents_left_unkillable"
  release_cleanup
  if [ -z "$problems" ]; then pass release.no_codex_cli "$v: Codex results $results, all direct; fakecodex (PATH, codex_path, switch) never run"
  else fail release.no_codex_cli "$problems"; fi
}
release_codex_check

info linger "$(loginctl show-user "$USER" -p Linger 2>&1)"
finish
