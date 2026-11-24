# shellcheck shell=bash
# Linux session lifecycle checks, sourced by check-linux.sh after lib.sh:
# what happens to the agent across a GNOME logout and login, without a
# desktop. Private session buses (dbus-daemon) and fake tray hosts
# (fakewatcher) stand in for the session; the fake server for the team
# server.
#   tray.bus_restart  the session bus is restarted at the same address
#                     (GNOME restarts it at every logout) while the agent
#                     runs: no crash, the same process registers again
#                     with the new bus's tray host, syncing goes on
#   tray.relogin      the bus goes away, a new session comes with a new
#                     bus, and autostart starts a second run: exactly one
#                     agent remains, registered on the new bus, showing
#                     the new percentage, syncing goes on
#   tray.late_host    the tray host appears after the start-up wait: the
#                     agent first runs without an icon, then registers at
#                     once
#   agent.log         the agent install started keeps a log file, with
#                     no token, key or home path in it
# Needs: $exe, $data, $out, $CI_BIN, wait_for, dev, stats, pass, fail,
# info, and the hooks start_agent (start the agent the way install does,
# with the variables given) and stop_agents (end every agent).

# lc_autostart VAR=value...: start "ai-usage run" the way the desktop's
# autostart does at login, in that environment.
lc_autostart() { env "$@" setsid "$exe" run < /dev/null >> "$out/autostart-run.log" 2>&1 & }

lc_log="$data/agent.log"
lc_pids() { pgrep -f "^$exe run" | tr '\n' ' ' | sed 's/ $//'; }
lc_count() { pgrep -f "^$exe run" | wc -l; }

# lc_bus NAME [ADDRESS]: start a private session bus; sets lc_addr, lc_bpid.
lc_bus() {
  local name=$1 addr=${2:-}
  : > "$out/$name.addr"; : > "$out/$name.pid"
  if [ -n "$addr" ]; then
    dbus-daemon --session --address="$addr" --fork --print-address=5 --print-pid=6 5>"$out/$name.addr" 6>"$out/$name.pid" || return 1
  else
    dbus-daemon --session --fork --print-address=5 --print-pid=6 5>"$out/$name.addr" 6>"$out/$name.pid" || return 1
  fi
  lc_ready() { lc_addr=$(head -n 1 "$out/$name.addr") && [ -n "$lc_addr" ] && lc_bpid=$(head -n 1 "$out/$name.pid") && [ -n "$lc_bpid" ]; }
  wait_for 10 lc_ready
}

# lc_watcher NAME ADDRESS: start a fake tray host on that bus; sets lc_wpid.
lc_watcher() {
  DBUS_SESSION_BUS_ADDRESS="$2" "$CI_BIN/fakewatcher" watch > "$out/$1.log" 2> "$out/$1.err" &
  lc_wpid=$!
  lc_wlog="$out/$1.log"
  lc_wready() { grep -q '^ready$' "$lc_wlog"; }
  wait_for 10 lc_wready
}

# lc_registered NAME: the item registered with that watcher; sets lc_svc.
lc_registered() { lc_svc=$(awk '$1 == "registered" { s = $2 } END { print s }' "$out/$1.log") && [ -n "$lc_svc" ]; }
lc_prop() { DBUS_SESSION_BUS_ADDRESS="$1" "$CI_BIN/fakewatcher" get "$2" "$3" 2>>"$out/lifecycle-get.err"; }
lc_device() { cat "$data/device-id" 2>/dev/null; }
lc_syncs() { dev "$(lc_device)" syncs; }
lc_more_syncs() { [ "$(lc_syncs)" -gt "$1" ] 2>/dev/null; }
lc_synced() { [ "$(lc_syncs)" -ge 1 ] 2>/dev/null; } # the device's first sync arrived
lc_weekly() { curl -fsS --max-time 10 -X POST "$BASE/weekly?percent=$1" > /dev/null; }
lc_panics() { if [ -f "$lc_log" ]; then grep -c '^panic\|^fatal error' "$lc_log"; else echo 0; fi; }

lifecycle_checks() {
  local sockdir sock a1 b1 w1 a2 b2 w2 a3 b3 w3 pid1 pid2 pid3 s problems label n
  # a socket path must be short (108 bytes)
  sockdir=$(mktemp -d) || { fail tray.bus_restart "no temporary folder"; return; }
  sock="$sockdir/bus"
  # login 1: the session bus at a fixed path, as /run/user/UID/bus
  if ! lc_bus bus1 "unix:path=$sock"; then fail tray.bus_restart "dbus-daemon did not start"; return; fi
  a1=$lc_addr; b1=$lc_bpid
  lc_watcher w1 "$a1" || { fail tray.bus_restart "watcher 1 not ready"; return; }
  w1=$lc_wpid
  lc_weekly 42
  start_agent XDG_SESSION_TYPE=x11 XDG_CURRENT_DESKTOP=ubuntu:GNOME DBUS_SESSION_BUS_ADDRESS="$a1"
  if ! wait_for 90 lc_registered w1; then
    fail tray.bus_restart "not registered on the first bus: $(tail -n 3 "$lc_log" 2>/dev/null)"
    kill "$w1" "$b1" 2>/dev/null; stop_agents; return
  fi
  pid1=$(lc_pids)
  # the first sync may come after the registration
  wait_for 60 lc_synced || { fail tray.bus_restart "no sync from device [$(lc_device)]"; kill "$w1" "$b1" 2>/dev/null; stop_agents; return; }

  # agent.log: the agent install started has a log file without secrets
  problems=""
  [ -s "$lc_log" ] || problems="$problems no_log"
  grep -q ' started (pid ' "$lc_log" 2>/dev/null || problems="$problems no_started_line"
  for bad in "$KEY" "$TOKEN" "$HOME/" "$EMAIL"; do
    if grep -qF -- "$bad" "$lc_log" 2>/dev/null; then problems="$problems secret_or_home_in_log"; fi
  done
  "$exe" status > "$out/status-lifecycle.txt" 2>&1
  grep -q '^Log:        ~/.config/ai-usage/agent.log$' "$out/status-lifecycle.txt" || problems="$problems status_has_no_log_line"
  if [ -z "$problems" ]; then pass agent.log "$(wc -l < "$lc_log") lines"; else fail agent.log "$problems"; fi

  # logout: GNOME restarts the session bus; the agent outlives it
  s=$(lc_syncs)
  kill "$w1" "$b1" 2>/dev/null
  sleep 2
  lc_weekly 50 # the label changes on the dead connection
  sleep 3
  rm -f "$sock"
  # (re-)login: a new bus at the same path, a tray host on it
  if ! lc_bus bus2 "unix:path=$sock"; then fail tray.bus_restart "dbus-daemon 2 did not start"; stop_agents; return; fi
  a2=$lc_addr; b2=$lc_bpid
  lc_watcher w2 "$a2" || { fail tray.bus_restart "watcher 2 not ready"; stop_agents; return; }
  w2=$lc_wpid
  problems=""
  wait_for 60 lc_registered w2 || problems="$problems not_registered_on_new_bus"
  pid2=$(lc_pids)
  [ "$(lc_count)" -eq 1 ] || problems="$problems processes=[$pid2]"
  [ "$pid2" = "$pid1" ] || problems="$problems pid_changed=$pid1->$pid2"
  label=""
  label_50() { label=$(lc_prop "$a2" "$lc_svc" XAyatanaLabel) && [ "$label" = 50% ]; }
  [ -n "$lc_svc" ] && { wait_for 30 label_50 || problems="$problems label=[$label]"; }
  wait_for 60 lc_more_syncs "$s" || problems="$problems syncs_stopped=$s"
  n=$(lc_panics); [ "$n" = 0 ] || problems="$problems panics=$n"
  grep -q 'restarting' "$lc_log" 2>/dev/null || problems="$problems no_restart_in_log"
  if [ -z "$problems" ]; then pass tray.bus_restart "pid $pid1 registered again on the restarted bus, syncs $s -> $(lc_syncs)"
  else fail tray.bus_restart "$problems; log: $(grep -v ' sync ' "$lc_log" 2>/dev/null | tail -n 4)"; fi

  cp "$lc_log" "$out/agent-bus-restart.log" 2>/dev/null || true
  stop_agents
  kill "$w2" "$b2" 2>/dev/null
  rm -rf "$sockdir"

  # a new agent on a session bus; logout; a login whose session bus has
  # another address, where autostart starts a second run while the old
  # one still runs; then the percentage changes
  local a0 b0 w0
  if ! lc_bus bus0; then fail tray.relogin "dbus-daemon 0 did not start"; return; fi
  a0=$lc_addr; b0=$lc_bpid
  lc_watcher w0 "$a0" || { fail tray.relogin "watcher 0 not ready"; return; }
  w0=$lc_wpid
  lc_weekly 42
  start_agent XDG_SESSION_TYPE=x11 XDG_CURRENT_DESKTOP=ubuntu:GNOME DBUS_SESSION_BUS_ADDRESS="$a0"
  if ! wait_for 90 lc_registered w0; then
    fail tray.relogin "not registered on the first bus"
    kill "$w0" "$b0" 2>/dev/null; stop_agents; return
  fi
  pid1=$(lc_pids)
  wait_for 60 lc_synced || { fail tray.relogin "no sync from device [$(lc_device)]"; kill "$w0" "$b0" 2>/dev/null; stop_agents; return; }
  s=$(lc_syncs)
  kill "$w0" "$b0" 2>/dev/null
  if ! lc_bus bus3; then fail tray.relogin "dbus-daemon 3 did not start"; stop_agents; return; fi
  a3=$lc_addr; b3=$lc_bpid
  lc_watcher w3 "$a3" || { fail tray.relogin "watcher 3 not ready"; stop_agents; return; }
  w3=$lc_wpid
  lc_autostart XDG_SESSION_TYPE=wayland XDG_CURRENT_DESKTOP=ubuntu:GNOME DBUS_SESSION_BUS_ADDRESS="$a3"
  sleep 1
  lc_weekly 57
  problems=""
  wait_for 60 lc_registered w3 || problems="$problems not_registered_on_new_bus"
  one_left() { [ "$(lc_count)" -eq 1 ]; }
  wait_for 30 one_left || problems="$problems processes=[$(lc_pids)]"
  pid3=$(lc_pids)
  [ "$pid3" != "$pid1" ] || problems="$problems old_agent_kept"
  label=""
  label_57() { label=$(lc_prop "$a3" "$lc_svc" XAyatanaLabel) && [ "$label" = 57% ]; }
  [ -n "$lc_svc" ] && { wait_for 30 label_57 || problems="$problems label=[$label]"; }
  wait_for 60 lc_more_syncs "$s" || problems="$problems syncs_stopped=$s"
  sleep 20 # still one agent, still registered, still syncing a while later
  [ "$(lc_count)" -eq 1 ] || problems="$problems processes_later=[$(lc_pids)]"
  [ "$(lc_pids)" = "$pid3" ] || problems="$problems agent_changed_later=[$(lc_pids)]"
  n=$(lc_panics); [ "$n" = 0 ] || problems="$problems panics=$n"
  if grep -q 'took over' "$lc_log" 2>/dev/null; then info relogin "the new run took over from the old one"; fi
  if [ -z "$problems" ]; then pass tray.relogin "pid $pid1 -> $pid3, registered on the new bus, label 57%, syncs $s -> $(lc_syncs)"
  else fail tray.relogin "$problems; log: $(grep -v ' sync ' "$lc_log" 2>/dev/null | tail -n 4)"; fi
  cp "$lc_log" "$out/agent-relogin.log" 2>/dev/null || true
  stop_agents
  kill "$w3" "$b3" 2>/dev/null

  # a tray host that comes after the start-up wait (here 3 s)
  if ! lc_bus bus4; then fail tray.late_host "dbus-daemon 4 did not start"; return; fi
  local a4=$lc_addr b4=$lc_bpid w4 t0 t1
  start_agent AI_USAGE_TRAY_WAIT=3s XDG_SESSION_TYPE=x11 XDG_CURRENT_DESKTOP=ubuntu:GNOME DBUS_SESSION_BUS_ADDRESS="$a4"
  problems=""
  headless_now() { [ "$(dev "$(lc_device)" last_mode)" = headless ]; }
  wait_for 30 headless_now || problems="$problems never_headless=$(dev "$(lc_device)" last_mode)"
  lc_watcher w4 "$a4" || problems="$problems watcher_not_ready"
  w4=$lc_wpid
  t0=$(date +%s)
  wait_for 15 lc_registered w4 || problems="$problems not_registered"
  t1=$(date +%s)
  tray_now() { [ "$(dev "$(lc_device)" last_mode)" = tray ]; }
  wait_for 30 tray_now || problems="$problems mode=$(dev "$(lc_device)" last_mode)"
  if [ -z "$problems" ]; then pass tray.late_host "registered $((t1 - t0)) s after the host appeared"
  else fail tray.late_host "$problems"; fi
  cp "$lc_log" "$out/agent-late-host.log" 2>/dev/null || true
  stop_agents
  kill "$w4" "$b4" 2>/dev/null
}
