#!/usr/bin/env bash
# Observe how the usage endpoint's rate limit behaves under the extension's
# polling. Run it while the installed extension is active.
#
#   watch_rate_limit.sh [PROBE_EVERY_S] [MAX_PROBES] [WATCH_S]
#
# Phase 1: probe every PROBE_EVERY_S (default 300) until the endpoint answers
#          200, at most MAX_PROBES (default 8) times.
# Phase 2: leave the extension polling for WATCH_S (default 1200) and count
#          the fresh "Rate limited" warnings it logged since the limit cleared.
#          The extension logs one each time it goes from OK to refused.
# Every probe is one request against the same limit, so keep them sparse.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
probe_every="${1:-300}"
max_probes="${2:-8}"
watch_s="${3:-1200}"

cleared=""
for i in $(seq "$max_probes"); do
    sleep "$probe_every"
    if out="$(/usr/bin/python3 "$here/probe_usage.py" 2>&1)"; then
        cleared="$(date '+%Y-%m-%d %H:%M:%S')"
        echo "limit cleared by $cleared (probe $i)"
        echo "$out" | grep -E 'HTTP|weekly'
        break
    fi
    echo "still limited at $(date +%H:%M:%S) (probe $i)"
done
if [ -z "$cleared" ]; then
    echo "still rate limited after $max_probes probes; giving up"
    exit 1
fi

sleep "$watch_s"
echo "--- $((watch_s / 60)) min of extension polling since the limit cleared"
echo "fresh rate-limit warnings: $(journalctl --user --no-pager --since "$cleared" 2>/dev/null \
    | grep -c 'Claude Usage: Rate limited')"
journalctl --user --no-pager --since "$cleared" 2>/dev/null \
    | grep -E 'Claude Usage|JS ERROR' | cut -c1-150 | tail -8
echo "--- final probe at $(date +%H:%M:%S)"
/usr/bin/python3 "$here/probe_usage.py" 2>&1 | grep -E 'HTTP|weekly'
