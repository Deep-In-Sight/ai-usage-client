#!/usr/bin/env bash
# The usage panel on a real headless GNOME Shell, inside check-panel-
# gnome.sh's private session (dbus-run-session, private HOME and XDG
# folders): plan Task 15 and the items carried to it (ledger "Carried to
# 4c"). PHASE 1 (one session):
#   panel.loads            the generated form for this shell, copied in
#                          before the start and enabled: state 1, no error,
#                          no JS error or warning of it, attached
#   panel.proxy_flags      its proxy has the no-auto-start flag (review m4)
#   panel.picture_combined fakepanel's combined fixture, the menu opened:
#                          five bars 42/13/7/100/8 % (pixcheck judge: ±2 px
#                          of one common length, the colours #d97757 ×4 and
#                          #f66151, the track rgba(128,128,128,0.32) over
#                          the background, the fill 8 px high, round ends),
#                          the critical row's percent text in #f66151; the
#                          server line wraps at the bars' width, nothing cut
#   panel.header_colour    a header's name in a row label's colour (review I1)
#   panel.open_redraw      a view with the same items and another percent
#                          while the menu is open: GetView again, the same
#                          item still open and hovered shows the new text;
#                          a click goes out (m5, review B-I2)
#   panel.click_pending    a view with other items while the menu is open:
#                          kept for the close, and a click made meanwhile
#                          still reaches the agent
#   panel.hides_without_agent, panel.returns
#   panel.picture_states   the stale Claude group's bars in their colour at
#                          0.6 (their track too), the problem line in the
#                          colour for the menu's measured background
#                          (#9a6700 light, #f5c211 dark: review B-I1)
#   panel.extension_failure badjson, extrafields, missing, hang, vanish
#                          (stage 0 Z4): the shell lives, judged by state
#                          and the button, not by logged, caught failures
#   panel.install          the one-line install with the shell running:
#                          this shell's form written, the relogin line once,
#                          the extension not read by the running shell
#   panel.tray_fallback    until then the tray icon, through the desktop's
#                          AppIndicator extension
#   panel.end_to_end       next login: turned on once, attached, no icon
#                          helper, a click reaches the agent; turned off by
#                          the user: an icon within 20 s (measured: the 10 s
#                          grace and a helper's start); next login: stays
#                          off; turned on again: attached again
#   panel.attach_accepted  the shell's Attach is taken by the bridge (it
#                          comes from the owner of org.gnome.Shell)
#   panel.lock             a lock longer than 100 s and the unlock: no icon
#                          helper; ActiveChanged comes from the shell (the
#                          owner of org.gnome.Shell.ScreenShield) or GNOME's
#                          forwarder org.gnome.ScreenSaver; the order of
#                          Detach, GetActive and ActiveChanged. GNOME 42:
#                          "not measurable on this runner" (no screen
#                          shield there), never a pass
#   panel.failure          a failure inside the extension: hidden, the shell
#                          lives, the icon back, no Attach again (also not
#                          after new views) until the agent's name went and
#                          came back
#   panel.session_end      what the bridge sees when the session ends: the
#                          shell ended (SIGTERM) and 5 s later the bus
# PHASE 2 (the next session, same folders, after its bus was away 35 s or
# more: an agent stopped by repeated failures in that time would show):
#   panel.relogin_same_agent the agent of the last session: its bridge
#                          finds the new bus and answers the extension's
#                          Attach within 8 s of org.gnome.Shell getting its
#                          owner, both seen on the bus (lost-bus ruling, 4b
#                          fix round 2; review B-I3); then the session ends
# PHASE 3 (the session after that: its bus is up without a shell for 35 s
# first, as when a user manager outlived the logout):
#   panel.relogin_shell_late the same agent's bridge said "no shell" and
#                          that it waits for GNOME Shell, and the same
#                          bridge process attaches within 15 s of the shell
#                          answering, the icons go (review A-M1, B-I3)
#   panel.relogin          a new agent logs in again (autostart): no icon
#                          helper before the panel attaches; the new agent
#                          is the one left (review B-M2)
#   panel.privacy          no request to a vendor, no fake token in the
#                          agent's files, the fake server's requests or this
#                          job's output (as check-linux.sh; review B-M10)
#   panel.remove           uninstall: the shell turns it off and out of its
#                          enabled list (which must be read), the folder
#                          goes, nothing of the agent is left
# Each verdict is a line of out/panel-results.txt (the workflow's
# notices); failures are also errors at once (lib.sh).
set -u
exec 2>&1
# Drives a shell and installs the agent on whatever session bus it is on:
# only inside check-panel-gnome.sh's private session on a CI runner.
if [ "${GITHUB_ACTIONS:-}" != true ] || [ "${PANEL_PRIVATE:-}" != 1 ] || [ -z "${RUNNER_TEMP:-}" ]; then
  echo "agent/ci/panel-session.sh: refusing to run outside check-panel-gnome.sh on GitHub Actions" >&2
  exit 2
fi
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=lib.sh
. "$here/lib.sh"

uuid=ai-usage-panel@localhost
evh=ai-usage-ci-eval@localhost
exts="$XDG_DATA_HOME/gnome-shell/extensions"
exe="$HOME/.local/bin/ai-usage"
data="$HOME/.config/ai-usage"
alog="$data/agent.log"
forms="$GITHUB_WORKSPACE/agent/internal/gnomeext/$FORM/$uuid"
bus="$DBUS_SESSION_BUS_ADDRESS"
phase=${PHASE:-1}
mkdir -p "$exts"

res() { printf '%s\n' "$*" >> "$out/panel-results.txt"; }
# verdict ID PROBLEMS DETAILS
verdict() {
  if [ -z "$2" ]; then pass "$1" "$3"; res "$1 pass ($LABEL): $3"
  else fail "$1" "$2 | $3"; res "$1 FAIL ($LABEL):$2 | $3"; fi
}
fact() { res "$1 fact ($LABEL): $2"; }
now() { date +%s.%N; }
since() { python3 -c "import sys; print('%.1f' % (float(sys.argv[2]) - float(sys.argv[1])))" "$1" "$(now)"; }
# wait_every STEP SECONDS COMMAND...: like wait_for, polling every STEP s
wait_every() {
  local step=$1 end
  end=$(python3 -c "import time,sys; print(time.time() + float(sys.argv[1]))" "$2"); shift 2
  while :; do
    "$@" && return 0
    python3 -c "import time,sys; sys.exit(0 if time.time() < float(sys.argv[1]) else 1)" "$end" || return 1
    sleep "$step"
  done
}
# wait_ms SECONDS COMMAND...: polling every 0.25 s
wait_ms() { wait_every 0.25 "$@"; }

# ------------------------------------------------------------ the shell
shell_eval() {
  gdbus call --session --dest org.gnome.Shell --object-path /org/gnome/Shell \
    --method org.gnome.Shell.Eval "$1" 2>&1
}
# ev JS: the value of a JS expression yielding a string or number
# ("ERR:..." when it failed)
ev() {
  local r
  r=$(shell_eval "$1")
  case "$r" in
    "(true, '"*"')") r=${r#"(true, '"}; r=${r%"')"}; r=${r#\"}; r=${r%\"}; printf '%s' "$r" ;;
    *) printf 'ERR:%s' "$(printf '%s' "$r" | tr '\n' ' ' | cut -c1-200)" ;;
  esac
}
alive() { [ "$(ev 1)" = 1 ] && [ "$(ev 'String(!!Main.panel.statusArea.dateMenu)')" = true ]; }
B="Main.panel.statusArea['$uuid']"
X="Main.extensionManager.lookup('$uuid')"
# JS helpers: find(actor, style class) -> actors; rect(actor) -> "x,y,w,h";
# rows(button) -> its limit rows' menu items
JS='const find = (a, cls) => { const out = []; const walk = x => { if (x.has_style_class_name && x.has_style_class_name(cls)) out.push(x); for (const c of x.get_children()) walk(c); }; if (a) walk(a); return out; }; const rect = a => { const [x, y] = a.get_transformed_position(); const [w, h] = a.get_transformed_size(); return [x, y, w, h].map(Math.round).join(","); }; const rows = b => b.menu._getMenuItems().filter(i => find(i, "ai-usage-row").length > 0);'
n=0
shell_pid=""
slog=""
shell_t="" # when the shell started last answered on the bus
start_shell() {
  n=$((n + 1))
  slog="$out/shell-$LABEL-$phase-$n.log"
  # shellcheck disable=SC2086
  gnome-shell --headless --virtual-monitor 1920x1080 $FLAGS > "$slog" 2>&1 &
  shell_pid=$!
  for _ in $(seq 120); do
    [ "$(ev 1)" = 1 ] && { shell_t=$(now); sleep 2; shell_eval 'Main.overview.hide()' > /dev/null; sleep 1; return 0; }
    kill -0 "$shell_pid" 2>/dev/null || return 1
    sleep 0.5
  done
  return 1
}
stop_shell() {
  [ -n "$shell_pid" ] || return 0
  kill "$shell_pid" 2>/dev/null
  for _ in $(seq 40); do kill -0 "$shell_pid" 2>/dev/null || break; sleep 0.25; done
  kill -9 "$shell_pid" 2>/dev/null
  wait "$shell_pid" 2>/dev/null
  shell_pid=""
  sleep 1
}
state() { ev "(() => { const e = $X; return e ? e.state + '|' + (e.error || '') : 'none'; })()"; }
# visible: true, false, absent (no button), or ERR:… (the shell did not
# answer: neither shown nor hidden; review B-M3)
visible() { ev "(() => { const b = $B; return b ? String(b.visible && (!b.container || b.container.visible)) : 'absent'; })()"; }
hidden_v() { [ "$1" = false ] || [ "$1" = absent ]; }
is_visible() { [ "$(visible)" = true ]; }
is_hidden() { hidden_v "$(visible)"; }
open_menu() { ev "(() => { $B.menu.open(); return 'open'; })()" > /dev/null; sleep 1.5; }
close_menu() { ev "(() => { const b = $B; if (b) b.menu.close(); return 1; })()" > /dev/null; sleep 0.5; }
shot() { # NAME: a screenshot in out/NAME.png
  gdbus call --session --dest org.gnome.Shell.Screenshot --object-path /org/gnome/Shell/Screenshot \
    --method org.gnome.Shell.Screenshot.Screenshot false false "$out/$1.png" > /dev/null 2>&1
}
# ext_errors LOG: shell log lines of an error or warning that name the extension
ext_errors() { grep -E 'JS ERROR|JS WARNING|CRITICAL|Exception' "$1" | grep -c -E "$uuid|ai-usage-panel" || true; }
owner_of() { gdbus call --session --dest org.freedesktop.DBus --object-path /org/freedesktop/DBus \
  --method org.freedesktop.DBus.GetNameOwner "$1" 2>/dev/null | sed -nE "s/^\('([^']*)',\)$/\1/p"; }
ext_call() { gdbus call --session --dest org.gnome.Shell.Extensions --object-path /org/gnome/Shell/Extensions \
  --method "org.gnome.Shell.Extensions.$1" "$uuid" 2>&1 | tr '\n' ' '; }
lists() { echo "enabled=$(gsettings get org.gnome.shell enabled-extensions | tr -d ' ') disabled=$(gsettings get org.gnome.shell disabled-extensions | tr -d ' ')"; }
set_lists() { # ENABLED-UUIDS...: the shell's lists for the next start
  local l="" u
  for u in "$@"; do l="$l${l:+, }'$u'"; done
  gsettings set org.gnome.shell enabled-extensions "[$l]"
  gsettings set org.gnome.shell disabled-extensions "[]"
}
base_exts() { echo "$evh" ${APPIND:+"$APPIND"}; }

# ------------------------------------------------------------ the agent
agent_count() { pgrep -f "^$exe run" | wc -l; }
icons() { icon_helper_count "$exe" "$bus"; }
no_icons() { [ "$(icons)" -eq 0 ]; }
some_icons() { [ "$(icons)" -ge 1 ]; }
attaches() { grep -c 'the panel extension attached' "$alog" 2>/dev/null || true; }
start_agent() { setsid "$exe" run < /dev/null >> "$out/autostart-run.log" 2>&1 & }
stop_agent() {
  pkill -TERM -f "^$exe run" 2>/dev/null
  gone() { [ "$(agent_count)" -eq 0 ] && [ "$(pgrep -f "^$exe tray-helper" | wc -l)" -eq 0 ]; }
  wait_for 20 gone || pkill -KILL -f "^$exe (run|tray-helper)" 2>/dev/null
}
# watcher_items: the items the desktop's tray (the AppIndicator extension) shows
watcher_items() {
  gdbus call --session --dest org.kde.StatusNotifierWatcher --object-path /StatusNotifierWatcher \
    --method org.freedesktop.DBus.Properties.Get org.kde.StatusNotifierWatcher RegisteredStatusNotifierItems 2>/dev/null |
    grep -o "'[^']*'" | grep -c . || true
}
# sample SECONDS FILE: icon helpers once a second; prints the most seen
sample() {
  local max=0 c i
  for i in $(seq "$1"); do
    c=$(icons)
    printf '%s %s\n' "$i" "$c" >> "$2"
    [ "$c" -gt "$max" ] && max=$c
    sleep 1
  done
  echo "$max"
}
# sample_children SECONDS FILE PARENT: the same for PARENT's icon helpers
sample_children() {
  local max=0 c i p
  for i in $(seq "$1"); do
    c=0
    for p in $(icon_helper_pids "$exe" "$bus"); do
      [ "$(ps -o ppid= -p "$p" | tr -d ' ')" = "$3" ] && c=$((c + 1))
    done
    printf '%s %s\n' "$i" "$c" >> "$2"
    [ "$c" -gt "$max" ] && max=$c
    sleep 1
  done
  echo "$max"
}
# enabled_list_has GVARIANT UUID: yes or no when GVARIANT (gsettings' value
# of a list of strings) parses and has UUID or not, else unreadable
enabled_list_has() {
  python3 - "$1" "$2" <<'PY'
import ast, sys
v = sys.argv[1].strip()
if v.startswith("@as "):
    v = v[4:]
try:
    l = ast.literal_eval(v)
    ok = isinstance(l, list) and all(isinstance(x, str) for x in l)
except Exception:
    ok = False
print(("yes" if sys.argv[2] in l else "no") if ok else "unreadable")
PY
}
# monitor FILE MATCH...: dbus-monitor in the background (sets mon_pid)
mon_pid=""
monitor() {
  local f=$1
  shift
  if command -v dbus-monitor > /dev/null; then
    dbus-monitor --session "$@" > "$f" 2>&1 &
    mon_pid=$!
    sleep 1
  else
    echo "no dbus-monitor" > "$f"
    mon_pid=""
  fi
}
unmonitor() { [ -n "$mon_pid" ] && { kill "$mon_pid" 2>/dev/null; wait "$mon_pid" 2>/dev/null; }; mon_pid=""; }
# monparse FILE T0: dbus-monitor's messages as "+seconds kind sender member args"
monparse() {
  python3 - "$1" "$2" <<'PY'
import re, sys
f, t0 = sys.argv[1], float(sys.argv[2])
msgs, cur = [], None
for line in open(f, errors="replace"):
    m = re.match(r"^(signal|method call|method return|error) time=([0-9.]+) sender=(\S+) -> destination=(\S+)(.*)$", line)
    if m:
        cur = {"kind": m.group(1), "t": float(m.group(2)) - t0, "sender": m.group(3), "dest": m.group(4), "rest": m.group(5), "args": []}
        msgs.append(cur)
        mm = re.search(r"member=(\w+)", cur["rest"]); cur["member"] = mm.group(1) if mm else ""
        s = re.search(r" serial=(\d+)", cur["rest"]); cur["serial"] = s.group(1) if s else ""
        r = re.search(r"reply_serial=(\d+)", cur["rest"]); cur["reply"] = r.group(1) if r else ""
    elif cur is not None and line.startswith("   ") and len(cur["args"]) < 3:
        cur["args"].append(line.strip().replace('"', ""))
calls = {}
for m in msgs:
    if m["kind"] == "method call":
        calls[(m["sender"], m["serial"])] = m
for m in msgs:
    if m["kind"] in ("method return", "error"):
        c = calls.get((m["dest"], m["reply"]))
        if c is None or c["member"] not in ("GetActive", "Attach", "Detach", "DisableExtension"):
            continue
        print("%+.3f return-of-%s %s %s" % (m["t"], c["member"], m["sender"], " ".join(m["args"][:1]) or m["rest"].strip()[:60]))
    elif m["member"] in ("Detach", "Attach", "GetView", "Ping", "Click", "GetActive", "ActiveChanged", "ViewChanged", "NameOwnerChanged", "Lock", "DisableExtension"):
        print("%+.3f %s %s %s %s" % (m["t"], m["kind"].replace("method ", ""), m["sender"], m["member"], " ".join(m["args"])))
PY
}

# ------------------------------------------------------------ setup
# the Eval helper: unsafe mode (Eval, Screenshot) in this throwaway shell;
# it stays on while the screen is locked (unlock-dialog), to unlock it
mkdir -p "$exts/$evh"
if [ "$MAJOR" = 42 ]; then
  cat > "$exts/$evh/extension.js" <<'EOF'
function init() {
    return {
        enable() { global.context.unsafe_mode = true; },
        disable() { global.context.unsafe_mode = false; },
    };
}
EOF
  sv='["42"]'
else
  cat > "$exts/$evh/extension.js" <<'EOF'
import {Extension} from 'resource:///org/gnome/shell/extensions/extension.js';
export default class CiEval extends Extension {
    enable() { global.context.unsafe_mode = true; }
    disable() { global.context.unsafe_mode = false; }
}
EOF
  sv='["46", "47", "48", "49", "50"]'
fi
printf '{"uuid": "%s", "name": "CI eval", "description": "CI only: unsafe mode in a throwaway shell", "shell-version": %s, "session-modes": ["user", "unlock-dialog"]}\n' "$evh" "$sv" > "$exts/$evh/metadata.json"
gsettings set org.gnome.shell welcome-dialog-last-shown-version '999' 2>/dev/null

fakelog="$out/fake-$LABEL.log"
fake_pid=""
fake_owns() { grep -c '^owns local.AIUsage.Panel$' "$fakelog" 2>/dev/null || true; }
start_fake() { # VIEW MODE
  local o
  o=$(fake_owns)
  "$CI_BIN/fakepanel" -view "$1" -mode "$2" -next "$out/next.json" -redraw "$out/redraw.json" -log "$fakelog" &
  fake_pid=$!
  more() { [ "$(fake_owns)" -gt "$o" ]; }
  wait_ms 10 more
}
stop_fake() { [ -n "$fake_pid" ] && { kill "$fake_pid" 2>/dev/null; wait "$fake_pid" 2>/dev/null; }; fake_pid=""; }
calls() { grep -c "^$1" "$fakelog" 2>/dev/null || true; }
cleanup() { unmonitor; stop_fake; stop_shell; }
trap cleanup EXIT

# judge_bars FILE combined|states: problems with the bars pixcheck finds
# in the menu (empty: as they should be: pixcheck judge, review B-M1), and
# what was found. The fixture's bars: Claude 42/13/7 % (stale in the
# states fixture: opacity 0.6), Codex 100 % critical and 8 %.
judge_bars() {
  local want="42:normal,13:normal,7:normal,100:critical,8:normal"
  [ "$2" = states ] && want="42:normal:0.6,13:normal:0.6,7:normal:0.6,100:critical,8:normal"
  "$CI_BIN/pixcheck" judge -want "$want" "$1" > "$1.bars" 2>&1
  python3 - "$1.bars" <<'PY'
import re, sys
bars, probs = [], "no problems line"
for l in open(sys.argv[1]):
    m = re.match(r"bar y=\d+ x=\d+ fill=(\d+) track=(\d+) colour=(#\w+) alpha=([0-9.]+) bg=(#\w+) base=(#\w+) trackcolour=(\S+) height=(\d+) ends=(\w+)", l)
    if m:
        bars.append("%s/%d:%s@%s(%s) track %s on %s h%s %s" % (m[1], int(m[1]) + int(m[2]), m[3], m[4], m[6], m[7], m[5], m[8], m[9]))
    elif l.startswith("problems: "):
        probs = l[len("problems: "):].strip()
print(("" if probs == "none" else probs) + "|" + "; ".join(bars))
PY
}

# end_session: the session ends as GNOME ends it (measured in phase 1):
# the shell is sent SIGTERM, its bus goes 5 s later; phase 1 publishes
# what the bus saw. Its verdicts are in phaseN.status (the bus ending ends
# dbus-run-session, phase 1's parent, too).
end_session() {
  monitor "$out/monitor-end-$LABEL-$phase.txt" "type='method_call',interface='local.AIUsage.Panel1'" "type='signal',sender='org.freedesktop.DBus',member='NameOwnerChanged'"
  local t0 tshell order dpid tbus
  t0=$(now)
  date '+%Y-%m-%d %H:%M:%S' > "$out/end-$LABEL-$phase.t0"
  kill -TERM "$shell_pid"
  for _ in $(seq 40); do kill -0 "$shell_pid" 2>/dev/null || break; sleep 0.25; done
  tshell=$(since "$t0")
  kill -9 "$shell_pid" 2>/dev/null; wait "$shell_pid" 2>/dev/null; shell_pid=""
  sleep 5
  order=$(monparse "$out/monitor-end-$LABEL-$phase.txt" "$t0" | grep -E 'Detach|Ping|NameOwnerChanged string (org.gnome.Shell|local.AIUsage.Panel|org.gnome.ScreenSaver) ' | head -n 8 | tr '\n' ';')
  dpid=$(gdbus call --session --dest org.freedesktop.DBus --object-path /org/freedesktop/DBus \
    --method org.freedesktop.DBus.GetConnectionUnixProcessID org.freedesktop.DBus 2>/dev/null | grep -o '[0-9]\+' | tail -n 1)
  tbus=$(since "$t0")
  [ "$phase" = 1 ] && res "panel.session_end fact ($LABEL): shell sent SIGTERM at 0 s, ended after ${tshell}s; seen on the bus (s): $order the bus ended by the script at ${tbus}s (pid ${dpid:-?})"
  echo 1 > "$out/phase$phase.done"
  finish > "$out/phase$phase.summary"; echo $? > "$out/phase$phase.status"
  cat "$out/phase$phase.summary"
  unmonitor
  # last: the bus goes; when, for check-panel-gnome.sh's next bus; the
  # bridge's view of it is read from agent.log in the next phase
  now > "$out/bus-$phase.gone"
  [ -n "$dpid" ] && kill "$dpid"
  exit 0
}

# ======================================================================
if [ "$phase" = 1 ]; then
# ------------------------------------------------------------ fake agent
python3 - "$GITHUB_WORKSPACE/agent/internal/tray/testdata/panel-combined.json" "$out/next.json" "$out/redraw.json" <<'PY'
import json, sys
v = json.load(open(sys.argv[1]))
v["sections"][1]["rows"] = v["sections"][1]["rows"][:1]  # r.codex.1 gone: other items
json.dump(v, open(sys.argv[2], "w"))
v = json.load(open(sys.argv[1]))
r = v["sections"][0]["rows"][0]  # r.claude.0: the same items, another percent
r["percent"], r["percent_text"] = 57, "57%"
json.dump(v, open(sys.argv[3], "w"))
PY
cp -r "$forms" "$exts/"
# shellcheck disable=SC2046
set_lists $(base_exts) "$uuid"
: > "$fakelog"
start_fake fixture:combined ok || echo "fakepanel did not start"
if ! start_shell; then
  # the shell's screen lock needs a display manager (fakegdm): if the
  # shell cannot start with one, it starts without (no lock to check)
  fact panel.shell_start "the shell did not start with ${FAKEGDM_PID:+fakegdm as the display manager}: $(grep -E 'JS ERROR|Error|rror:|xception|@resource|Gjs-|CRITICAL' "$slog" | head -n 12 | tr '\n' ' ' | cut -c1-950)"
  if [ -n "${FAKEGDM_PID:-}" ]; then
    kill "$FAKEGDM_PID" 2>/dev/null
    sleep 1
    start_shell && fact panel.shell_start "it started without a display manager (the lock is not checked)"
  fi
fi
if [ -z "$shell_pid" ] || ! kill -0 "$shell_pid" 2>/dev/null; then
  verdict panel.loads " shell_did_not_start" "$(grep -E 'JS ERROR|rror|xception' "$slog" | head -n 5 | tr '\n' ' ' | cut -c1-600)"
  finish; exit 1
fi
wait_ms 20 is_visible
st=$(state); vis=$(visible)
has_attach() { [ "$(calls Attach)" -ge 1 ]; }
wait_ms 10 has_attach
p=""
[ "$st" = "1|" ] || p="$p state=[$st]"
[ "$vis" = true ] || p="$p visible=$vis"
[ "$(calls Attach)" -ge 1 ] || p="$p no_attach"
e=$(ext_errors "$slog"); [ "$e" -eq 0 ] || p="$p log_errors=$e"
verdict panel.loads "$p" "$SHELL_VERSION form $FORM: state $st, visible $vis, $(grep -m1 '^Attach' "$fakelog"), log errors $e"
[ "$e" -eq 0 ] || fact panel.loads "$(grep -E 'JS ERROR|JS WARNING|CRITICAL|Exception' "$slog" | grep -E "$uuid|ai-usage-panel" | head -n 3 | tr '\n' ' ' | cut -c1-600)"

pf=$(ev "(() => { const c = $X.stateObj._client; return c && c._proxy ? String(c._proxy.g_flags) : 'none'; })()")
p=""
case "$pf" in ''|*[!0-9]*) p=" flags=[$pf]" ;; *) [ $((pf & 4)) -ne 0 ] || p=" flags=$pf" ;; esac
verdict panel.proxy_flags "$p" "the proxy's flags $pf (4: DO_NOT_AUTO_START) on GNOME $MAJOR"

# ------------------------------------------------------------ pictures
open_menu
shot "panel-combined-$LABEL"
dark=$(ev "String($B._aiuDark)")
mr=$(ev "(() => { $JS return rect($B.menu.box); })()")
jb=$(judge_bars "$out/panel-combined-$LABEL.png" combined)
e=$(grep -c "$uuid: menu" "$slog" || true)
p="${jb%%|*}"; [ "$e" -eq 0 ] || p="$p menu_errors=$e"
# the critical row's percent text in #f66151, a normal row's not (B-M1);
# the labels are found by the fixture's texts (Codex 100 %, Claude 42 %),
# not by the extension's own style classes
pt=$(ev "(() => { $JS const box = $B.menu.box; const pc = find(box, 'ai-usage-percent'); const c = pc.filter(x => x.text === '100%')[0]; const n = pc.filter(x => x.text === '42%')[0]; return [c ? rect(c) : 'none', n ? rect(n) : 'none', c ? c.text : '-'].join(' '); })()")
read -r crect nrect ctext <<< "$pt"
cpx=$("$CI_BIN/pixcheck" colour -tol 8 -rect "$crect" "$out/panel-combined-$LABEL.png" '#f66151' 2>&1)
npx=$("$CI_BIN/pixcheck" colour -tol 8 -rect "$nrect" "$out/panel-combined-$LABEL.png" '#f66151' 2>&1)
case "$cpx" in ''|*[!0-9]*) p="$p critical_text=[$pt $cpx]" ;; *) [ "$cpx" -ge 10 ] || p="$p critical_text_pixels=$cpx" ;; esac
case "$npx" in ''|*[!0-9]*) p="$p normal_text=[$npx]" ;; *) [ "$npx" -eq 0 ] || p="$p normal_text_red=$npx" ;; esac
# the server line (found by the fixture's text) wraps at the bars' width
# and loses nothing (ruling A-I2): given twice its text, it stays at most
# 300 px wide, takes two lines or more and is not cut short
srv=$(python3 -c "import json, sys; print(json.dumps(json.load(open(sys.argv[1]))['server']))" "$GITHUB_WORKSPACE/agent/internal/tray/testdata/panel-combined.json")
sl=$(ev "(() => { const want = $srv; const out = []; const walk = x => { if (x.clutter_text && x.text === want) out.push(x); for (const c of x.get_children()) walk(c); }; walk($B.menu.box); const l = out[0]; if (!l) return 'none'; const t = l.clutter_text; const one = l.get_preferred_height(-1)[1]; l.text = want + ' ' + want; const w = l.get_preferred_width(-1)[1]; const h = l.get_preferred_height(w)[1]; let cut = 'n/a'; try { cut = String(t.get_layout().is_ellipsized()); } catch (e) { } l.text = want; return [Math.round(w), Math.round(h), Math.round(one), String(t.line_wrap), cut, Math.round(l.width)].join(' '); })()")
read -r sw sh s1 swrap scut snow <<< "$sl"
case "$sw$sh$s1" in
  ''|*[!0-9]*) p="$p server_line=[$sl]" ;;
  *)
    [ "$swrap" = true ] || p="$p server_line_does_not_wrap"
    [ "$sw" -le 301 ] || p="$p server_line_${sw}px_wide"
    [ "$sh" -ge $((s1 * 18 / 10)) ] || p="$p server_line_one_line=${sh}/${s1}"
    [ "$scut" != true ] || p="$p server_line_cut"
    ;;
esac
verdict panel.picture_combined "${p:+ $p}" "bars ${jb#*|}; critical percent [$ctext] $cpx px of #f66151, a normal percent $npx; server line twice as long: ${sw:-?}x${sh:-?} px (a line ${s1:-?}, wraps ${swrap:-?}, cut ${scut:-?}, shown ${snow:-?} px wide); menu $mr; the extension's dark flag: $dark; picture panel-combined-$LABEL.png"

# review I1: the header's name has a row label's colour (no :insensitive)
hc=$(ev "(() => { $JS const b = $B; const n = find(b.menu.box, 'ai-usage-name')[0]; const l = find(b.menu.box, 'ai-usage-label')[0]; const fg = a => { const c = a.get_theme_node().get_foreground_color(); return [c.red, c.green, c.blue, c.alpha].join('/'); }; let it = n; while (it && !(it.has_style_class_name && it.has_style_class_name('popup-menu-item'))) it = it.get_parent(); return [rect(n), rect(l), fg(n), fg(l), n.opacity, l.opacity, it ? (it.get_style_pseudo_class() || '-') : 'none'].join(' '); })()")
read -r nr lr nfg lfg nop lop pseudo <<< "$hc"
ni=$("$CI_BIN/pixcheck" ink -rect "$nr" "$out/panel-combined-$LABEL.png" 2>&1)
li=$("$CI_BIN/pixcheck" ink -rect "$lr" "$out/panel-combined-$LABEL.png" 2>&1)
p=$(python3 - "$ni" "$li" <<'PY'
import sys
def ink(s):
    try:
        h = s.split()[0].split("=")[1].lstrip("#")
        return [int(h[i:i + 2], 16) for i in (0, 2, 4)]
    except Exception:
        return None
a, b = ink(sys.argv[1]), ink(sys.argv[2])
print("" if a and b and max(abs(x - y) for x, y in zip(a, b)) <= 20 else " ink_differs")
PY
)
[ "$nfg" = "$lfg" ] || p="$p theme_colour=$nfg/$lfg"
[ "$nop" = "$lop" ] || p="$p opacity=$nop/$lop"
case "$pseudo" in *insensitive*) p="$p pseudo=$pseudo" ;; esac
verdict panel.header_colour "$p" "name ink [$ni] label ink [$li]; theme colour $nfg vs $lfg; opacity $nop vs $lop; header pseudo-class [$pseudo]"

# review m5, B-I2: a view with the same items and another percent while
# the menu is open (fakepanel -redraw: r.claude.0 42 % -> 57 %)
t1=$(ev "(() => { $JS const r = rows($B); global._aiuCi = r[0]; r[0].active = true; return find(r[0], 'ai-usage-percent')[0].text; })()")
g0=$(calls GetView); kill -USR1 "$fake_pid"
more_gv() { [ "$(calls GetView)" -gt "$g0" ]; }
wait_ms 10 more_gv; sleep 1
g1=$(calls GetView)
kept=$(ev "(() => { $JS const b = $B; const r = rows(b); const pc = find(r[0], 'ai-usage-percent')[0]; return [b.menu.isOpen, r[0] === global._aiuCi, r[0].active, pc.text, pc.is_mapped() && pc.opacity > 0].join(' '); })()")
c0=$(calls 'Click r.claude.0$')
ev "(() => { global._aiuCi.activate(null); return 'clicked'; })()" > /dev/null
more_click() { [ "$(calls 'Click r.claude.0$')" -gt "$c0" ]; }
wait_ms 5 more_click; clicked=$(calls 'Click r.claude.0$')
p=""
[ "$g1" -gt "$g0" ] || p="$p no_GetView_after_ViewChanged"
[ "$t1" = "42%" ] || p="$p text_before=[$t1]"
[ "$kept" = "true true true 57% true" ] || p="$p open,same,hovered,text,shown=[$kept]"
[ "$clicked" -gt "$c0" ] || p="$p no_click"
verdict panel.open_redraw "$p" "ViewChanged with the menu open: GetView ×$((g1 - g0)); the first row's percent $t1 before; after: open,same item,hovered,percent,shown = $kept; a row click then: Click r.claude.0 ×$((clicked - c0))"
verdict panel.click "$( [ "$clicked" -gt "$c0" ] || echo ' no_click')" "activate on the first row: $(grep -m1 '^Click' "$fakelog")"
close_menu

# a view with other items while the menu is open: built when it closes;
# a click made meanwhile still reaches the agent
open_menu
i0=$(ev "String($B.menu._getMenuItems().length)")
g0=$(calls GetView); kill -USR2 "$fake_pid"
wait_ms 10 more_gv; sleep 1
pend=$(ev "(() => { const b = $B; return [b.menu.isOpen, b._aiuPending !== null].join(' '); })()")
c0=$(calls 'Click r.claude.0$'); r0=$(calls 'Click .* refused')
ev "(() => { $JS rows($B)[0].activate(null); return 'clicked'; })()" > /dev/null
wait_ms 5 more_click; sleep 1
after=$(ev "(() => { const b = $B; return [b.menu.isOpen, b._aiuPending === null, b.menu._getMenuItems().length].join(' '); })()")
p=""
[ "$pend" = "true true" ] || p="$p open,pending=[$pend]"
[ "$(calls 'Click r.claude.0$')" -gt "$c0" ] || p="$p no_click"
[ "$(calls 'Click .* refused')" -eq "$r0" ] || p="$p refused"
[ "$after" = "false true $((i0 - 1))" ] || p="$p closed,built,items=[$after] (before $i0)"
verdict panel.click_pending "$p" "items $i0; other items while open: open,pending=$pend; a click then: Click r.claude.0 ×$(( $(calls 'Click r.claude.0$') - c0 )); after it: open,built,items=$after"

# the agent goes and comes back
t0=$(now); stop_fake
wait_ms 5 is_hidden; hid=$(visible); th=$(since "$t0")
verdict panel.hides_without_agent "$(hidden_v "$hid" || echo " not_hidden=[$hid]")" "visible=$hid ${th}s after fakepanel ended"
a0=$(calls Attach)
start_fake fixture:states ok
wait_ms 10 is_visible
more_attach() { [ "$(calls Attach)" -gt "$a0" ]; }
wait_ms 10 more_attach
p=""; is_visible || p=" hidden"; [ "$(calls Attach)" -gt "$a0" ] || p="$p no_second_attach"
verdict panel.returns "$p" "visible=$(visible), Attach in the log $(calls Attach)"

open_menu
shot "panel-states-$LABEL"
dark=$(ev "String($B._aiuDark)")
mr=$(ev "(() => { $JS return rect($B.menu.box); })()")
probs=$(ev "(() => { $JS return String(find($B.menu.box, 'ai-usage-problem').filter(p => p.text !== '').length); })()")
prect=$(ev "(() => { $JS const l = find($B.menu.box, 'ai-usage-problem').filter(p => p.text !== '')[0]; return l ? rect(l) : 'none'; })()")
# light or dark from the menu's background pixel, not the extension's own
# flag (review B-I1): #9a6700 on light, #f5c211 on dark
pr=$("$CI_BIN/pixcheck" problem -menu "$mr" -rect "$prect" "$out/panel-states-$LABEL.png" 2>&1)
np=$(printf '%s' "$pr" | sed -nE 's/.* pixels=([0-9]+)$/\1/p')
jb=$(judge_bars "$out/panel-states-$LABEL.png" states)
e=$(grep -c "$uuid: menu" "$slog" || true)
p="${jb%%|*}"
case "$np" in ''|*[!0-9]*) p="$p problem=[$pr]" ;; *) [ "$np" -gt 10 ] || p="$p problem_pixels=$np" ;; esac
[ "${probs:-0}" -ge 1 ] 2>/dev/null || p="$p problem_lines=[$probs]"
[ "$e" -eq 0 ] || p="$p menu_errors=$e"
verdict panel.picture_states "${p:+ $p}" "bars ${jb#*|}; problem lines $probs, the menu: $pr in the line $prect (the extension's dark flag: $dark); picture panel-states-$LABEL.png"
close_menu

# ------------------------------------------------------------ failures (Z4)
fsum=""; p=""
for mode in badjson extrafields missing hang vanish; do
  stop_fake; sleep 1
  d0=$(calls Detach); a0=$(calls Attach)
  start_fake fixture:combined "$mode"
  case "$mode" in
    badjson)
      sleep 10; v=$(visible)
      [ "$(calls Detach)" -gt "$d0" ] || p="$p badjson:no_detach"
      hidden_v "$v" || p="$p badjson:not_hidden=[$v]" ;;
    extrafields|missing)
      sleep 10; v=$(visible)
      [ "$v" = true ] || p="$p $mode:hidden"
      [ "$(calls Detach)" -eq "$d0" ] || p="$p $mode:detach"
      [ "$(calls Attach)" -gt "$a0" ] || p="$p $mode:no_attach" ;;
    hang)
      sleep 10; v=$(visible)
      hidden_v "$v" || p="$p hang:not_hidden=[$v]"
      sleep 25; alive || p="$p hang:shell_dead_after_the_timeout"
      v="$v,$(visible)" ;;
    vanish)
      wait_ms 20 more_attach; sleep 8; v1=$(visible); sleep 12; v="$v1,$(visible)"
      hidden_v "$v1" || p="$p vanish:not_hidden_while_away=[$v1]"
      [ "$(visible)" = true ] || p="$p vanish:not_back"
      [ "$(calls Attach)" -ge $((a0 + 2)) ] || p="$p vanish:no_reattach" ;;
  esac
  st=$(state); al=alive; alive || { al=dead; p="$p $mode:shell_dead"; }
  [ "$st" = "1|" ] || p="$p $mode:state=[$st]"
  fsum="$fsum $mode: shell $al, state ${st%%|*}, visible $v, Detach +$(( $(calls Detach) - d0 )), Attach +$(( $(calls Attach) - a0 ));"
done
stop_fake
verdict panel.extension_failure "$p" "$fsum"

# ------------------------------------------------------------ the agent
# a user who never had the panel: no folder, not in the shell's lists
stop_shell
rm -rf "${exts:?}/$uuid"
# shellcheck disable=SC2046
set_lists $(base_exts)
fake_login file
fake_codex_login
start_shell || { verdict panel.install " shell_did_not_start" ""; finish; exit 1; }
timeout 180 sh -c "wget -qO- $BASE/files/install-ci.sh | sh" > "$out/install-panel-$LABEL.log" 2>&1
rc=$?
lines=$(grep -c 'The AI Usage panel appears after you log out and log in again.' "$out/install-panel-$LABEL.log" || true)
d=$(diff -r "$forms" "$exts/$uuid" 2>&1 | head -n 3 | tr '\n' ' ')
st=$(state)
not_read() { grep -q 'the shell has not read the panel extension yet' "$alog" 2>/dev/null; }
wait_for 60 not_read
p=""
[ "$rc" -eq 0 ] || p="$p rc=$rc"
[ "$lines" = 1 ] || p="$p relogin_lines=$lines"
[ -d "$exts/$uuid" ] && [ -z "$d" ] || p="$p folder=[${d:-missing}]"
[ "$st" = none ] || p="$p running_shell_has_it=[$st]"
not_read || p="$p bridge_did_not_try"
[ ! -e "$data/gnome-panel-enabled" ] || p="$p enabled_marker"
fact panel.install_bridge "$(grep -E '\[tray shell\]' "$alog" | head -n 6 | cut -c21- | tr '\n' ';' | cut -c1-700)"
verdict panel.install "$p" "rc $rc, relogin line ×$lines, folder = $FORM: $([ -z "$d" ] && echo yes || echo "no: $d"), running shell: $st; bridge: $(grep -m1 -o '\[tray shell\] GNOME Shell .*' "$alog" 2>/dev/null)"
icons_up() { some_icons && [ "$(watcher_items)" -ge 1 ]; }
wait_for 90 icons_up
verdict panel.tray_fallback "$(icons_up || echo " icons=$(icons) items=$(watcher_items)")" "icon helpers $(icons), items in the desktop's tray $(watcher_items) (${APPIND:-no AppIndicator extension})"

# next login: the bridge turns the extension on once; it attaches
stop_agent
stop_shell
start_shell
monitor "$out/monitor-attach-$LABEL.txt" "type='method_call',interface='local.AIUsage.Panel1'" "type='method_return'" "type='error'"
t0=$(now)
start_agent
attached() { [ "$(state)" = "1|" ] && [ -e "$data/gnome-panel-enabled" ] && [ -e "$data/gnome-panel-attached" ] && no_icons; }
wait_for 45 attached
ta=$(since "$t0")
unmonitor
shell_owner=$(owner_of org.gnome.Shell)
att=$(monparse "$out/monitor-attach-$LABEL.txt" "$t0" | grep -E ' (call|return-of-Attach|error)' | grep -E 'Attach' | head -n 3 | tr '\n' ';')
p=""
attached || p="$p not_attached(state=$(state) enabled=$([ -e "$data/gnome-panel-enabled" ] && echo y) attached=$([ -e "$data/gnome-panel-attached" ] && echo y) icons=$(icons))"
[ "$(attaches)" -ge 1 ] || p="$p bridge_saw_no_attach"
ad=$(grep -c -E 'Attach failed|AccessDenied' "$slog" || true); [ "$ad" -eq 0 ] || p="$p refused=$ad"
[ -z "$p" ] || fact panel.attach_bridge "$(grep -E '\[tray shell\]|usage panel' "$alog" | tail -n 8 | cut -c21- | tr '\n' ';' | cut -c1-800)"
verdict panel.attach_accepted "$p" "Attach from $shell_owner (the owner of org.gnome.Shell): [$att]; the bridge: $(grep -m1 -o 'the panel extension attached.*' "$alog"); tray icons $(icons) after ${ta}s"
e2e=""; attached || e2e="$e2e login:not_attached"
dev=$(cat "$data/device-id" 2>/dev/null)
f0=$(dev "$dev" want_fresh)
ev "(() => { const i = $B.menu._getMenuItems().find(m => m.label && m.label.text === 'Refresh now'); if (!i) return 'none'; i.activate(null); return 'clicked'; })()" > "$out/refresh-click.txt"
fresh() { [ "$(dev "$dev" want_fresh)" -gt "${f0:-0}" ] 2>/dev/null; }
wait_for 90 fresh || e2e="$e2e refresh_not_heard($(cat "$out/refresh-click.txt"))"
e2e_detail="login: attached after ${ta}s, extension on (gnome-panel-enabled), no icon helper; Refresh now: want_fresh ${f0:-0} -> $(dev "$dev" want_fresh)"

# ------------------------------------------------------------ lock
# the shell's screen saver is org.gnome.Shell.ScreenShield; the public
# org.gnome.ScreenSaver is GNOME's forwarder, started on demand (by the
# Lock call below, as Super+L's gnome-settings-daemon starts it)
saver=$(owner_of org.gnome.Shell.ScreenShield)
if [ -z "$saver" ] && [ "$MAJOR" = 42 ]; then
  # GNOME 42 headless makes no screen shield on a runner (no login
  # session for it, check-panel-gnome.sh): not a pass, said as such
  res "panel.lock not measurable on this runner ($LABEL): GNOME 42's headless shell has no screen shield here (org.gnome.Shell.ScreenShield not owned; $(grep -m1 -i 'screenshield' "$slog" | cut -c1-200)); covered by the owner's check on the real Ubuntu 22.04 PC (lock and unlock: the panel stays, no tray icon)"
elif [ -z "$saver" ]; then
  verdict panel.lock " no_screen_shield" "org.gnome.Shell.ScreenShield not owned on GNOME $MAJOR, which has one on this runner"
else
  monitor "$out/monitor-lock-$LABEL.txt" "type='signal',interface='org.gnome.ScreenSaver'" "type='method_call',interface='local.AIUsage.Panel1'" \
    "type='method_call',interface='org.gnome.ScreenSaver'" "type='method_return'" "type='error'"
  a0=$(attaches)
  : > "$out/lock-icons-$LABEL.txt"
  t0=$(now)
  lk=$(timeout 20 gdbus call --session --dest org.gnome.ScreenSaver --object-path /org/gnome/ScreenSaver --method org.gnome.ScreenSaver.Lock 2>&1 | tr '\n' ' ')
  locked=$(ev 'String(Main.screenShield.locked) + "," + String(Main.screenShield.active) + "," + Main.sessionMode.currentMode')
  fwd=$(owner_of org.gnome.ScreenSaver)
  m1=$(sample 110 "$out/lock-icons-$LABEL.txt")
  during=$(ev 'String(Main.screenShield.active) + "," + Main.sessionMode.currentMode')
  tu=$(now)
  ul=$(ev '(() => { Main.screenShield.deactivate(false); return "unlocking"; })()')
  m2=$(sample 25 "$out/lock-icons-$LABEL.txt")
  unmonitor
  back=$(visible)
  order=$(monparse "$out/monitor-lock-$LABEL.txt" "$t0" | grep -E 'Lock|Detach|GetActive|ActiveChanged|Attach' | grep -v 'return-of-Detach' | head -n 12 | tr '\n' ';')
  senders=$(monparse "$out/monitor-lock-$LABEL.txt" "$t0" | awk '$2 == "signal" && $4 == "ActiveChanged" { print $3 }' | sort -u | tr '\n' ' ')
  p=""
  case "$locked" in *unlock-dialog*) ;; *) p="$p not_locked=[$locked $lk]" ;; esac
  [ "$m1" -eq 0 ] || p="$p icon_helper_while_locked=$m1"
  [ "$m2" -eq 0 ] || p="$p icon_helper_at_unlock=$m2"
  [ "$back" = true ] || p="$p panel_not_back"
  [ "$(attaches)" -gt "$a0" ] || p="$p no_reattach"
  [ -n "$senders" ] || p="$p no_ActiveChanged"
  case " $senders " in *" $saver "*) ;; *) p="$p no_ActiveChanged_from_the_shell" ;; esac
  for s in $senders; do [ "$s" = "$saver" ] || [ "$s" = "$fwd" ] || p="$p ActiveChanged_from_$s"; done
  verdict panel.lock "$p" "locked 110 s then unlocked: icon helpers at most $m1 while locked, $m2 after; state while locked [$locked], at the end [$during]; ActiveChanged from [$senders]: the shell's org.gnome.Shell.ScreenShield is $saver (also org.gnome.Shell: $(owner_of org.gnome.Shell)), the forwarder org.gnome.ScreenSaver ${fwd:-not running}; Lock answered [$lk]; panel back $back, attached again $(( $(attaches) - a0 ))×"
  fact panel.lock_order "seconds from the Lock call: $order"
  fact panel.lock_bridge "$(grep -E '\[tray shell\] (the panel extension|the screen is locked|no Ping)' "$alog" | tail -n 4 | tr '\n' ';' | cut -c1-600)"
fi

# ------------------------------------------------------------ a failure inside the extension
monitor "$out/monitor-failure-$LABEL.txt" "type='method_call',interface='local.AIUsage.Panel1'" "type='signal',interface='local.AIUsage.Panel1'"
a0=$(attaches)
t0=$(now)
fl=$(ev "(() => { const c = $X.stateObj._client; c.guard('ci', () => { throw new Error('a failure made by CI'); }); return 'failed'; })()")
wait_ms 5 is_hidden; hid=$(visible)
got_icons=no; wait_for 30 some_icons && got_icons=yes; ti=$(since "$t0")
curl -fsS --max-time 10 -X POST "$BASE/weekly?percent=57" > /dev/null 2>&1
views() { monparse "$out/monitor-failure-$LABEL.txt" "$t0" | grep -c ' signal .* ViewChanged' || true; }
some_views() { [ "$(views)" -ge 1 ]; }
wait_for 120 some_views
sleep 5
nv=$(views)
ext_calls=$(monparse "$out/monitor-failure-$LABEL.txt" "$t0" | grep -c -E ' call .* (Attach|GetView)' || true)
still=$(visible)
ba=$(attaches)
# the agent's name goes and comes back: the bridge is ended, the agent starts it again
bp=$(bridge_pids "$bus" | head -n 1)
tb=$(now)
[ -n "$bp" ] && kill "$bp"
reattached() { [ "$(attaches)" -gt "$ba" ] && is_visible && no_icons; }
wait_for 60 reattached
tr2=$(since "$tb")
unmonitor
curl -fsS --max-time 10 -X POST "$BASE/weekly?percent=42" > /dev/null 2>&1
p=""
[ "$fl" = failed ] || p="$p eval=[$fl]"
hidden_v "$hid" || p="$p not_hidden=[$hid]"
alive || p="$p shell_dead"
[ "$got_icons" = yes ] || p="$p no_icons"
[ "$nv" -ge 1 ] || p="$p no_new_view_seen"
[ "$ext_calls" -eq 0 ] || p="$p extension_called_$ext_calls"
hidden_v "$still" || p="$p shown_again=[$still]"
[ "$ba" -eq "$a0" ] || p="$p attached_again_before_the_name_went"
reattached || p="$p not_back_after_the_name_came_back"
verdict panel.failure "$p" "hidden ($hid), shell alive, an icon helper after ${ti}s; $nv ViewChanged and $ext_calls Attach/GetView calls of the extension since; the bridge ended (pid ${bp:-none}): back and attached after ${tr2}s"

# ------------------------------------------------------------ the user turns it off
t0=$(now)
dis=$(ext_call DisableExtension)
offline() { some_icons && [ ! -e "$data/gnome-panel-attached" ]; }
# polled every 0.5 s; the limit (not a design value: the 10 s grace, the
# bridge's 2 s lock settle and an icon helper's start) is 20 s, the time
# measured is published (review B-M4)
wait_every 0.5 30 offline; toff=$(since "$t0")
offline || e2e="$e2e off:icons=$(icons),marker=$([ -e "$data/gnome-panel-attached" ] && echo kept || echo gone)"
python3 -c "import sys; sys.exit(0 if float(sys.argv[1]) <= 20 else 1)" "$toff" || e2e="$e2e off:after_${toff}s"
# next login: it stays off
turned_on() { grep -c 'turned the panel extension on' "$alog" 2>/dev/null || true; }
on0=$(turned_on)
stop_agent; stop_shell; start_shell
start_agent
wait_for 30 some_icons
sleep 10
st=$(state)
[ "$st" != "1|" ] || e2e="$e2e turned_on_again"
some_icons || e2e="$e2e relogin_off:no_icons"
[ "$(turned_on)" -eq "$on0" ] || e2e="$e2e enabled_again"
l=$(lists)
# turned on again by the user: the panel comes back
en=$(ext_call EnableExtension)
wait_for 40 attached || e2e="$e2e on_again:not_attached"
verdict panel.end_to_end "$e2e" "$e2e_detail; off: $dis -> icon after ${toff}s, gnome-panel-attached gone; next login: state ${st%%|*}, lists $l, icons $(icons); on again: $en, attached $(attached && echo yes || echo no)"

end_session
fi

# ====================================================================== phase 2
# the next session: the same folders, a new bus after the last one was away
# BUS_AWAY seconds (check-panel-gnome.sh keeps it away 35 s or more); the
# agent of the last session is still running, its bridge waiting for its
# bus (it dials again every 2 s)
if [ "$phase" = 2 ]; then
end=$(cat "$out/end-$LABEL-1.t0" 2>/dev/null)
res "panel.session_end fact ($LABEL): the bridge in agent.log since: $(awk -v t="$end" '$0 >= t' "$alog" | grep -E '\[tray shell\]|usage panel' | head -n 8 | cut -c12- | tr '\n' ';' | cut -c1-700)"
a0=$(attaches)
# the attach moment is the bus's: the bridge's answer to Attach, against
# the moment org.gnome.Shell got its owner, both as dbus-monitor saw them
# (review of fix round 3: the polling below is slower than the attach)
monitor "$out/monitor-relogin-$LABEL.txt" "type='method_call',interface='local.AIUsage.Panel1',member='Attach'" "type='method_return'" "type='signal',sender='org.freedesktop.DBus',member='NameOwnerChanged',arg0='org.gnome.Shell'"
start_shell || { verdict panel.relogin_same_agent " shell_did_not_start" ""; finish; exit 1; }
# this session's bus is at the runtime folder's address, where the bridge
# looks; the clock starts when the shell answers (review B-I3)
same=$(pgrep -f "^$exe run" | tr '\n' ' ')
again() { [ "$(attaches)" -gt "$a0" ] && [ "$(state)" = "1|" ] && is_visible; }
wait_ms 30 again
ts=$(since "$shell_t")
unmonitor
# seconds from the shell's owner to the first answer to an Attach after it
ta=$(monparse "$out/monitor-relogin-$LABEL.txt" 0 | awk '
  $4 == "NameOwnerChanged" && $6 == "org.gnome.Shell" && $NF ~ /^:/ && own == "" { own = $1 + 0 }
  $2 == "return-of-Attach" && own != "" && att == "" { att = $1 + 0 }
  END { if (own != "" && att != "") printf "%.1f", att - own }')
p=""
[ -n "$same" ] || p=" no_agent_of_the_last_session"
again || p="$p not_attached"
if [ -z "$ta" ]; then
  p="$p no_attach_seen_on_the_bus"
else
  python3 -c "import sys; sys.exit(0 if float(sys.argv[1]) <= 8 else 1)" "$ta" || p="$p attach_${ta}s_after_the_shell"
fi
python3 -c "import sys; sys.exit(0 if float(sys.argv[1]) >= 35 else 1)" "${BUS_AWAY:-0}" || p="$p bus_away_only_${BUS_AWAY:-?}s"
verdict panel.relogin_same_agent "$p" "the bus away ${BUS_AWAY:-?}s; the agent of the last session (pid $(echo $same)) attached ${ta:-?}s after org.gnome.Shell got its owner (on the bus; polled: ${ts}s after the shell answered); its bridge: $(awk -v t="$end" '$0 >= t' "$alog" | grep -E '\[tray shell\] (.*waiting for the session bus|the session bus is there|GNOME Shell [0-9]|the panel extension attached)' | head -n 4 | cut -c12- | tr '\n' ';' | cut -c1-500)"
end_session
fi

# ====================================================================== phase 3
# the session after that: its bus is up without a shell for 35 s first (a
# user manager that outlived the logout keeps the bus; the shell comes with
# the next login), then the shell; the same agent serves it
end=$(cat "$out/end-$LABEL-2.t0" 2>/dev/null)
a0=$(attaches)
up=${BUS_UP:-$(now)}
# the bridge reaches this bus and, 30 s later, says "no shell"; the shell
# comes when the bus has been up 35 s and the bridge has said it
said() { awk -v t="$end" '$0 >= t' "$alog" | grep -E '\[tray shell\] no usage panel on this desktop.*\(no shell\)' | head -n 1 | cut -c12-; }
has_said() { [ -n "$(said)" ]; }
wait_for 70 has_said
# the bridge that said it: the same one must serve the shell (it waits on
# in its process, review A-M1), and it says it waits for the shell
bpid=$(bridge_pids "$bus" | head -n 1)
rest=$(python3 -c "import sys,time; print(max(0.0, 35 - (time.time() - float(sys.argv[1]))))" "$up")
sleep "$rest"
before=$(said)
noshell=$(since "$up")
start_shell || { verdict panel.relogin_shell_late " shell_did_not_start" ""; finish; exit 1; }
again() { [ "$(attaches)" -gt "$a0" ] && [ "$(state)" = "1|" ] && is_visible; }
wait_ms 30 again
ts=$(since "$shell_t")
wait_ms 15 no_icons
apid=$(bridge_pids "$bus" | head -n 1)
p=""
[ -n "$before" ] || p=" no_shell_not_seen_by_the_bridge"
case "$before" in *"waiting for GNOME Shell"*) ;; *) [ -z "$before" ] || p="$p not_waiting_for_the_shell" ;; esac
[ -n "$bpid" ] && [ "$apid" = "$bpid" ] || p="$p bridge_pid=${bpid:-none}_then_${apid:-none}"
python3 -c "import sys; sys.exit(0 if float(sys.argv[1]) >= 35 else 1)" "$noshell" || p="$p no_shell_only_${noshell}s"
again || p="$p not_attached"
python3 -c "import sys; sys.exit(0 if float(sys.argv[1]) <= 15 else 1)" "$ts" || p="$p after_${ts}s"
no_icons || p="$p icon_helpers=$(icons)"
verdict panel.relogin_shell_late "$p" "${noshell}s on the bus without a shell: the bridge (pid ${bpid:-none}) said [$before]; attached ${ts}s after the shell answered, the bridge then pid ${apid:-none}, icon helpers $(icons); its bridge: $(awk -v t="$end" '$0 >= t' "$alog" | grep -E '\[tray shell\] (the session bus is there|no usage panel|GNOME Shell [0-9]|the panel extension attached)' | head -n 5 | cut -c12- | tr '\n' ';' | cut -c1-500)"

: > "$out/relogin-icons-$LABEL.txt"
old=" $(pgrep -f "^$exe run" | tr '\n' ' ') "
t0=$(now)
start_agent
newpid=""
new_agent() { local p; for p in $(pgrep -f "^$exe run"); do case "$old" in *" $p "*) ;; *) newpid=$p; return 0 ;; esac; done; return 1; }
wait_ms 5 new_agent
m=$(sample_children 30 "$out/relogin-icons-$LABEL.txt" "${newpid:-0}")
left=$(pgrep -f "^$exe run" | tr '\n' ' ' | sed 's/ $//')
p=""
[ "$m" -eq 0 ] || p=" icon_helper=$m"
[ "$(state)" = "1|" ] && is_visible || p="$p not_attached=[$(state) $(visible)]"
[ "$(agent_count)" -eq 1 ] || p="$p agents=$(agent_count)"
[ -n "$newpid" ] || p="$p no_new_agent"
[ -n "$newpid" ] && [ "$left" = "$newpid" ] || p="$p survivor=[$left](new_${newpid:-none})"
verdict panel.relogin "$p" "the agent of the last session: $(echo $old); the new one ${newpid:-none}, left running: [$left]; the marker gnome-panel-attached $([ -e "$data/gnome-panel-attached" ] && echo there || echo gone) at login; icon helpers at most $m in 30 s; panel $(visible); agents $(agent_count)"

# no request to Anthropic or OpenAI, no fake token anywhere (the checks of
# check-linux.sh: run.vendors_off, fakeserver.token_leaks, files.token_leaks;
# also this job's output, which is kept as an artefact)
p=""
vendors_off "$data" || p="$p no_vendors_off_line"
lk=$(stats | jget - token_leaks)
[ "$lk" = 0 ] || p="$p token_leaks=[$lk]"
h=$(no_fake_tokens "$data") || p="$p token_in_files=[$h]"
ho=$(no_fake_tokens "$out") || p="$p token_in_output=[$ho]"
verdict panel.privacy "$p" "agent.log: $(grep -m1 -o 'CI build: no request to Anthropic or OpenAI' "$alog"); the fake server's token leaks: $lk; no fake token in $(ls "$data" | wc -l) files of the agent nor in $(find "$out" -type f | wc -l) files of out/"

"$exe" uninstall > "$out/uninstall-panel-$LABEL.log" 2>&1
rc=$?
sleep 3
st=$(state); b=$(ev "String(!!$B)")
en=$(gsettings get org.gnome.shell enabled-extensions 2>&1)
p=""
[ "$rc" -eq 0 ] || p="$p rc=$rc"
[ ! -e "$exts/$uuid" ] || p="$p folder_left"
[ "$st" != "1|" ] || p="$p still_on"
[ "$b" = false ] || p="$p button_left"
# the enabled list must be read (a list of strings, "@as []" when empty),
# not an error that happens not to name the uuid (review of fix round 3)
pl=$(enabled_list_has "$en" "$uuid")
case "$pl" in
  no) ;;
  yes) p="$p still_in_enabled_extensions" ;;
  *) p="$p enabled_list_unreadable=[$(printf '%s' "$en" | tr '\n' ' ' | cut -c1-120)]" ;;
esac
[ "$(agent_count)" -eq 0 ] && [ "$(pgrep -f "^$exe tray-helper" | wc -l)" -eq 0 ] || p="$p processes_left"
[ ! -e "$data" ] || p="$p data_left"
alive || p="$p shell_dead"
verdict panel.remove "$p" "rc $rc; state ${st%%|*}, button $b, folder $([ -e "$exts/$uuid" ] && echo left || echo gone); $(lists); stderr [$(grep -i 'panel extension' "$out/uninstall-panel-$LABEL.log" | head -n 1 | cut -c1-200)]"
stop_shell
finish
