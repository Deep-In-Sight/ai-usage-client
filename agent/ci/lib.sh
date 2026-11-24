# shellcheck shell=bash
# Shared by check-linux.sh and check-macos.sh. Every check has an id;
# a failed required check becomes an ::error annotation, information an
# ::notice (few: Actions keeps at most 10 of each per step), and the last
# line is "::notice title=summary::<passed>/<required>".
# Env: CI_DIST (release files), CI_BIN (fakeserver), CI_SCRIPTS (agent/ci).
set -u
# These checks overwrite the Claude and Codex sign-ins, install and remove
# the agent, and (the release check) change the hosts file, the trusted
# CAs and a sysctl: only ever on a GitHub Actions runner, never a PC.
if [ "${GITHUB_ACTIONS:-}" != true ] || [ -z "${RUNNER_TEMP:-}" ]; then
  echo "agent/ci: refusing to run: these checks overwrite sign-ins and system settings and run only on a GitHub Actions runner (GITHUB_ACTIONS=true, RUNNER_TEMP set)" >&2
  exit 2
fi
BASE=http://127.0.0.1:18700
KEY=ci-ingest-key-0123456789
TOKEN=fake-access-token-0123456789
EMAIL=user-a@example.com
# The fake Codex sign-in: its JWTs end in CODEX_SIG (a leak of either
# token shows it), its refresh token is CODEX_REFRESH.
CODEX_SIG=fake-codex-sig-ci-0123456789
CODEX_REFRESH=fake-codex-refresh-ci-0123456789
CODEX_EMAIL=user-ci@example.com
CODEX_ACCOUNT=00000000-0000-4000-8000-000000000001
out="${GITHUB_WORKSPACE:-$PWD}/out"
mkdir -p "$out"
required=0
passed=0
failed_ids=""

oneline() { printf '%s' "$*" | tr '\r\n' '  ' | sed 's/%/%25/g' | cut -c1-300; }
pass() { required=$((required + 1)); passed=$((passed + 1)); echo "pass $1: $(oneline "${2:-}")"; }
fail() { required=$((required + 1)); failed_ids="$failed_ids $1"; echo "::error title=$1::$(oneline "${2:-}")"; }
info() { echo "::notice title=$1::$(oneline "${2:-}")"; }
finish() {
  echo "::notice title=summary::$passed/$required${failed_ids:+ failed:$failed_ids}"
  [ "$passed" -eq "$required" ]
}

# jget FILE|- KEY...: a value from JSON ("" when missing).
jget() {
  python3 -c 'import json,sys
try:
    v = json.load(open(sys.argv[1])) if sys.argv[1] != "-" else json.load(sys.stdin)
    for k in sys.argv[2:]:
        v = v[int(k)] if isinstance(v, list) else v[k]
    print(json.dumps(v) if isinstance(v, (dict, list)) else ("" if v is None else v))
except Exception:
    print("")' "$@"
}
stats() { curl -fsS --max-time 10 "$BASE/stats"; }
# dev DEVICE FIELD: a field of one device in /stats.
dev() { stats | jget - devices "$1" "$2"; }
# wait_for SECONDS COMMAND...: true once COMMAND succeeds.
wait_for() {
  local limit=$1; shift
  local end=$(( $(date +%s) + limit ))
  while :; do
    "$@" && return 0
    [ "$(date +%s)" -ge "$end" ] && return 1
    sleep 2
  done
}

# fake_login [file]: the fake account in ~/.claude.json, and with "file" the
# fake sign-in in ~/.claude/.credentials.json (expires in 8 hours).
fake_creds_json() {
  printf '{"claudeAiOauth":{"accessToken":"%s","expiresAt":%s000,"subscriptionType":"max"}}' "$TOKEN" $(( $(date +%s) + 28800 ))
}
fake_login() {
  printf '{"oauthAccount":{"accountUuid":"acc-ci-1","emailAddress":"%s","displayName":"User A","organizationName":"Example"}}\n' "$EMAIL" > "$HOME/.claude.json"
  if [ "${1:-}" = file ]; then
    mkdir -p "$HOME/.claude"
    fake_creds_json > "$HOME/.claude/.credentials.json"
  fi
}

# fake_codex_login: a fake ChatGPT sign-in in ~/.codex/auth.json (fake
# JWTs, the access token expires in 9 days; account and user ids of the
# plan: 00000000-0000-4000-8000-000000000001, user-ci).
fake_codex_login() {
  mkdir -p "$HOME/.codex"
  python3 - "$HOME/.codex/auth.json" "$CODEX_SIG" "$CODEX_REFRESH" "$CODEX_EMAIL" "$CODEX_ACCOUNT" <<'PY'
import base64, json, sys, time
path, sig, refresh, email, account = sys.argv[1:6]
b64 = lambda o: base64.urlsafe_b64encode(json.dumps(o).encode()).rstrip(b"=").decode()
auth = {"chatgpt_account_id": account, "chatgpt_user_id": "user-ci", "chatgpt_plan_type": "plus"}
jwt = lambda claims: b64({"alg": "none"}) + "." + b64(claims) + "." + sig
access = jwt({"exp": int(time.time()) + 9 * 86400, "https://api.openai.com/auth": auth})
idt = jwt({"email": email, "https://api.openai.com/auth": auth})
json.dump({"auth_mode": "chatgpt", "OPENAI_API_KEY": None, "last_refresh": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
           "tokens": {"access_token": access, "id_token": idt, "refresh_token": refresh, "account_id": account}}, open(path, "w"))
PY
  chmod 600 "$HOME/.codex/auth.json"
}
# codex_aside / codex_back: the Codex sign-in out of the way of a check
# that wants Claude alone, and back.
codex_aside() { [ -d "$HOME/.codex" ] && mv "$HOME/.codex" "$HOME/.codex-aside"; true; }
codex_back() { [ -d "$HOME/.codex-aside" ] && mv "$HOME/.codex-aside" "$HOME/.codex"; true; }

# start_fakeserver [PORT [FLAGS...]]: the fake team server (default 18700).
start_fakeserver() {
  local port=${1:-18700}; shift 2>/dev/null
  "$CI_BIN/fakeserver" -listen "127.0.0.1:$port" -key "$KEY" -token "$TOKEN,$CODEX_SIG,$CODEX_REFRESH" -files "$CI_DIST" \
    -scripts "$CI_SCRIPTS" "$@" > "$out/fakeserver-$port.log" 2>&1 &
  wait_for 20 curl -fsS "http://127.0.0.1:$port/healthz" -o /dev/null
}

# codex_ok DEVICE: the device's last Codex block was signed in (ok) with
# the fake Codex account.
codex_ok() {
  [ "$(dev "$1" providers | jget - codex last_auth_state)" = ok ] &&
    [ "$(dev "$1" providers | jget - codex last_email)" = "$CODEX_EMAIL" ]
}
# both_in_status EXE: `status` shows Claude's part and Codex's, with the
# Codex rows the fake server's latest gives (5-hour 42 %, weekly 17 %).
both_in_status() {
  local s
  s=$("$1" status 2>&1)
  printf '%s\n' "$s" > "$out/status-both.txt"
  printf '%s' "$s" | grep -q '^Claude:     signed in' &&
    printf '%s' "$s" | grep -q '^Codex:      signed in (Plus)' &&
    printf '%s' "$s" | grep -Eq '^ +5-hour +42%' &&
    printf '%s' "$s" | grep -Eq '^ +Weekly +17%'
}
# no_fake_tokens DIR: no fake token (Claude's or Codex's) in any file
# under DIR (status.json, backlog.json, latest-*.json, state.json,
# agent.log); prints the files that have one.
no_fake_tokens() {
  local hits
  hits=$(grep -rlF -e "$TOKEN" -e "$CODEX_SIG" -e "$CODEX_REFRESH" "$1" 2>/dev/null)
  [ -z "$hits" ] || { printf '%s' "$hits" | tr '\n' ' '; return 1; }
}

# vendors_off DATA: the CI build's log says it sends nothing to Anthropic
# or OpenAI (every lane's request is refused before it leaves the runner).
vendors_off() { grep -q 'CI build: no request to Anthropic or OpenAI' "$1/agent.log" 2>/dev/null; }

# sync_ok DEVICE MODE: the device synced at least twice, signed in, with the
# fake account, this build's version and MODE.
sync_ok() {
  local s
  s=$(stats | jget - devices "$1") || return 1
  [ -n "$s" ] || return 1
  [ "$(printf %s "$s" | jget - syncs)" -ge 2 ] 2>/dev/null &&
    [ "$(printf %s "$s" | jget - last_auth_state)" = ok ] &&
    [ "$(printf %s "$s" | jget - last_email)" = "$EMAIL" ] &&
    [ "$(printf %s "$s" | jget - last_version)" = 0.0.0-ci ] &&
    [ "$(printf %s "$s" | jget - last_mode)" = "$2" ]
}

# The release check (check-linux.sh release_codex_check) records here what
# it changed on the runner; release_cleanup undoes it, each part once, and
# is also the exit trap, so an interrupted run undoes it too.
REL_HOSTS_SAVED="" REL_CA="" REL_PORT_OLD="" REL_PATH_OLD="" REL_SWITCH="" REL_VPID="" REL_AGENTS_LEFT=""

# release_stop_agents: no agent or tray helper of the installed executable
# ($exe) is left running (re-review R-3): the service stopped, then
# SIGTERM, then SIGKILL. False if one still runs.
release_stop_agents() {
  local left
  agents_left() { pgrep -f "^$exe (run|tray-helper)" | wc -l | tr -d ' '; }
  systemctl --user stop ai-usage.service > /dev/null 2>&1
  pkill -f "^$exe (run|tray-helper)" 2>/dev/null
  none_running() { [ "$(agents_left)" -eq 0 ]; }
  wait_for 10 none_running || { pkill -KILL -f "^$exe (run|tray-helper)" 2>/dev/null; wait_for 5 none_running; }
  left=$(agents_left)
  [ "$left" -eq 0 ]
}

# release_cleanup: first every agent the check started is ended; only then
# are the real host names given back (a release-style agent left running
# would otherwise reach the real vendors with the fake sign-ins). If one
# cannot be ended, the names stay mapped to this runner, where nothing
# answers once the fake vendors are stopped.
release_cleanup() {
  if [ -n "$REL_HOSTS_SAVED" ] && ! release_stop_agents; then
    REL_AGENTS_LEFT=1
    echo "::error title=release cleanup::an agent of the release check still runs: the vendor names stay mapped to this runner"
  fi
  [ -n "$REL_VPID" ] && kill "$REL_VPID" 2>/dev/null
  REL_VPID=""
  pkill -f -- "-listen 127.0.0.1:18702" 2>/dev/null
  if [ -n "$REL_SWITCH" ]; then
    [ -n "$REL_PATH_OLD" ] && systemctl --user set-environment PATH="$REL_PATH_OLD"
    systemctl --user unset-environment AI_USAGE_TEST_CODEX_FALLBACK 2>/dev/null
    REL_SWITCH=""
  fi
  if [ -z "$REL_AGENTS_LEFT" ] && [ -n "$REL_HOSTS_SAVED" ] && [ -f "$REL_HOSTS_SAVED" ]; then
    sudo cp "$REL_HOSTS_SAVED" /etc/hosts && rm -f "$REL_HOSTS_SAVED"
    REL_HOSTS_SAVED=""
  fi
  if [ -n "$REL_CA" ]; then
    sudo rm -f "$REL_CA" && sudo update-ca-certificates --fresh > /dev/null 2>&1
    REL_CA=""
  fi
  if [ -n "$REL_PORT_OLD" ]; then
    sudo sysctl -q -w net.ipv4.ip_unprivileged_port_start="$REL_PORT_OLD"
    REL_PORT_OLD=""
  fi
  true
}
