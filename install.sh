#!/usr/bin/env bash
# Install, update or remove the Claude Usage GNOME Shell extension for this user.
#
#   ./install.sh              copy the extension into ~/.local/share and enable it
#   ./install.sh --uninstall  disable it and remove it again
#
# Nothing needs root. GNOME Shell only reads extension code when it starts, so
# after a first install or an update, restart the shell once:
#   X11:      Alt+F2, type r, Enter
#   Wayland:  log out and back in
# Logs: journalctl --user -b -g "Claude Usage"   (or /usr/bin/gnome-shell)
set -euo pipefail

uuid="claude-usage@localhost"
src_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$uuid"
ext_dir="${XDG_DATA_HOME:-$HOME/.local/share}/gnome-shell/extensions/$uuid"
# The system tools, not a conda/venv copy that cannot see the desktop's settings.
gsettings=/usr/bin/gsettings
gnome_extensions=/usr/bin/gnome-extensions

enabled_list() { "$gsettings" get org.gnome.shell enabled-extensions; }

if [[ "${1:-}" == "--uninstall" ]]; then
    "$gnome_extensions" disable "$uuid" 2>/dev/null || true
    rm -rf "$ext_dir"
    echo "Removed $uuid (it disappears from the top bar at the next shell restart if still shown)."
    exit 0
fi

mkdir -p "$ext_dir"
install -m 644 "$src_dir"/metadata.json "$src_dir"/extension.js "$src_dir"/usage.js \
    "$src_dir"/stylesheet.css "$src_dir"/clawd.svg "$ext_dir/"

if "$gnome_extensions" enable "$uuid" 2>/dev/null; then
    echo "Installed and enabled $uuid."
else
    # The running shell has not seen the extension yet: put it on the enabled
    # list so it loads at the next shell start.
    current="$(enabled_list)"
    if [[ "$current" != *"'$uuid'"* ]]; then
        if [[ "$current" == "@as []" ]]; then
            "$gsettings" set org.gnome.shell enabled-extensions "['$uuid']"
        else
            "$gsettings" set org.gnome.shell enabled-extensions "${current%]}, '$uuid']"
        fi
    fi
    echo "Installed $uuid; it loads at the next shell start."
fi
echo "Restart GNOME Shell to load new code: Alt+F2, r, Enter (X11) or log out and in (Wayland)."
