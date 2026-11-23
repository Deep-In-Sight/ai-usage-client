#!/usr/bin/env bash
# Render the extension in a throwaway headless GNOME Shell and save a screenshot.
#
#   preview_shell.sh OUT.png [open|closed]     (default: popup open)
#
# Nothing here touches the running desktop: the preview shell gets its own
# D-Bus session, its own settings (XDG_CONFIG_HOME), its own extensions folder
# (XDG_DATA_HOME) and its own runtime dir, all deleted afterwards. HOME is
# kept, so the extension reads the real Claude Code sign-in and shows real
# numbers. Set PREVIEW_HOME to point it at another home (e.g. an empty folder
# to see the signed-out state).
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
out="$(realpath -m "${1:?usage: preview_shell.sh OUT.png [open|closed]}")"
state="${2:-open}"

work="$(mktemp -d)"
cleanup() {
    # Helper services started on the private bus (document portal, gvfs) keep
    # FUSE mounts under run/ for a moment after the bus goes away.
    for _ in 1 2 3 4 5 6 7 8; do
        grep -q " $work/run/" /proc/mounts || break
        sleep 0.5
    done
    for mount in "$work/run/doc" "$work/run/gvfs"; do
        fusermount3 -u -z "$mount" 2>/dev/null || true
    done
    rm -rf "$work" 2>/dev/null || true
}
trap cleanup EXIT
mkdir -p "$work/config" "$work/cache" "$work/data/gnome-shell/extensions" "$work/run"
chmod 700 "$work/run"
cp -r "$here/../claude-usage@localhost" "$work/data/gnome-shell/extensions/"
# The robot icon is not in the source tree (see install.sh); borrow the
# installed extension's copy so the preview looks like the real thing.
installed_robot="${XDG_DATA_HOME:-$HOME/.local/share}/gnome-shell/extensions/claude-usage@localhost/clawd.svg"
if [ -f "$installed_robot" ]; then
    cp "$installed_robot" "$work/data/gnome-shell/extensions/claude-usage@localhost/"
fi

# PREVIEW_CACHE_FILE: start the preview with this file as the extension's
# saved "last successful reply" (to see how old numbers are shown).
if [ -n "${PREVIEW_CACHE_FILE:-}" ]; then
    mkdir -p "$work/cache/claude-usage"
    cp "$PREVIEW_CACHE_FILE" "$work/cache/claude-usage/last.json"
fi

# Helper extension, preview only: unsafe mode lets this script drive the
# preview shell over D-Bus (open the popup, take the screenshot).
helper="$work/data/gnome-shell/extensions/preview-helper@localhost"
mkdir -p "$helper"
cat > "$helper/metadata.json" <<'EOF'
{"uuid": "preview-helper@localhost", "name": "Preview helper",
 "description": "Enables unsafe mode in the preview shell", "shell-version": ["46"]}
EOF
cat > "$helper/extension.js" <<'EOF'
import {Extension} from 'resource:///org/gnome/shell/extensions/extension.js';
export default class PreviewHelper extends Extension {
    enable() { global.context.unsafe_mode = true; }
    disable() { global.context.unsafe_mode = false; }
}
EOF

# Clean environment: no conda, no DISPLAY, no pointers to the real session.
# Services the private bus starts on its own (tracker, portals) are noisy on
# stderr; that goes to a log shown only if the preview fails.
if ! env -i HOME="${PREVIEW_HOME:-$HOME}" USER="$USER" LANG="${LANG:-C.UTF-8}" PATH=/usr/bin:/bin \
    XDG_CONFIG_HOME="$work/config" XDG_DATA_HOME="$work/data" XDG_RUNTIME_DIR="$work/run" \
    XDG_CACHE_HOME="$work/cache" \
    XDG_DATA_DIRS=/usr/share/ubuntu:/usr/share/gnome:/usr/local/share:/usr/share \
    XDG_CURRENT_DESKTOP=ubuntu:GNOME PREVIEW_WAIT="${PREVIEW_WAIT:-5}" \
    PREVIEW_EVAL="${PREVIEW_EVAL:-}" \
    /usr/bin/dbus-run-session -- /bin/bash "$here/preview_session.sh" "$out" "$state" \
    2> "$work/session.err"; then
    tail -40 "$work/session.err" >&2
    exit 1
fi
