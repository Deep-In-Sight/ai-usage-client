#!/usr/bin/env bash
# Inner half of preview_shell.sh: runs inside the private D-Bus session.
set -euo pipefail
exec 2>&1       # this script's own errors go to stdout; stderr is the services' noise

out="$1"
state="$2"

gsettings set org.gnome.shell enabled-extensions \
    "['claude-usage@localhost', 'preview-helper@localhost']"
gsettings set org.gnome.desktop.interface color-scheme "${PREVIEW_SCHEME:-prefer-dark}"

gnome-shell --headless --wayland --mode=ubuntu --virtual-monitor 1920x1080 \
    > "$XDG_RUNTIME_DIR/shell.log" 2>&1 &
shell=$!
trap 'kill "$shell" 2>/dev/null || true' EXIT

shell_eval() {
    gdbus call --session --dest org.gnome.Shell --object-path /org/gnome/Shell \
        --method org.gnome.Shell.Eval "$1"
}

ready=no
for _ in $(seq 60); do
    if shell_eval '1' 2>/dev/null | grep -q "^(true"; then
        ready=yes
        break
    fi
    kill -0 "$shell" 2>/dev/null || break
    sleep 0.5
done
if [ "$ready" != yes ]; then
    echo "preview shell did not come up:" >&2
    tail -30 "$XDG_RUNTIME_DIR/shell.log" >&2
    exit 1
fi

shell_eval 'Main.overview.hide()' > /dev/null
sleep "$PREVIEW_WAIT"       # let the first fetch land
if [ "$state" = open ]; then
    shell_eval "Main.panel.statusArea['claude-usage@localhost'].menu.open()"
    sleep 1.5               # past the bar animation
fi
gdbus call --session --dest org.gnome.Shell.Screenshot \
    --object-path /org/gnome/Shell/Screenshot \
    --method org.gnome.Shell.Screenshot.Screenshot false false "$out"

# Optional: run some JS in the preview shell after the screenshot (e.g. to
# trigger an action), then report whether the indicator is still there.
if [ -n "${PREVIEW_EVAL:-}" ]; then
    echo "--- PREVIEW_EVAL"
    shell_eval "$PREVIEW_EVAL"
    sleep 1.5
    echo "indicator present: $(shell_eval "!!Main.panel.statusArea['claude-usage@localhost']")"
    echo "enabled list: $(gsettings get org.gnome.shell enabled-extensions)"
fi

saved="$XDG_CACHE_HOME/claude-usage/last.json"
if [ -f "$saved" ]; then
    echo "saved reply: $(wc -c < "$saved") bytes"
else
    echo "saved reply: none"
fi

echo "--- shell log lines about the extension"
grep -i -E 'claude|JS ERROR|JS WARNING' "$XDG_RUNTIME_DIR/shell.log" | head -20 || true
