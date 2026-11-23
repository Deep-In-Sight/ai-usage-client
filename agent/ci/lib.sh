# shellcheck shell=bash
# Shared by check-linux.sh and check-macos.sh. Every check has an id;
# a failed required check becomes an ::error annotation, information an
# ::notice (few: Actions keeps at most 10 of each per step), and the last
# line is "::notice title=summary::<passed>/<required>".
# Env: CI_DIST (release files), CI_BIN (fakeserver), CI_SCRIPTS (agent/ci).
set -u
BASE=http://127.0.0.1:18700
KEY=ci-ingest-key-0123456789
TOKEN=fake-access-token-0123456789
EMAIL=user-a@example.com
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

start_fakeserver() {
  "$CI_BIN/fakeserver" -listen 127.0.0.1:18700 -key "$KEY" -token "$TOKEN" -files "$CI_DIST" -scripts "$CI_SCRIPTS" \
    > "$out/fakeserver.log" 2>&1 &
  wait_for 20 curl -fsS "$BASE/healthz" -o /dev/null
}

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
