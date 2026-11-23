// Claude Usage: weekly plan usage in the GNOME top bar.
//
// Top bar: the Claude Code robot, a ring gauge and the weekly (all models)
// percentage, refreshed every 5 minutes. Click: a card with two bars, weekly
// usage for all models and for one model (Fable), each with its reset time,
// and a Quit item. Opening the card queries fresh numbers straight away.
//
// The card always shows the last successful query: a failed one (offline,
// rate limited, sign-in expired) leaves the numbers in place and only adds a
// small note. The last good reply is also kept on disk, so it is there right
// after a login or restart.
//
// Data comes from the account-usage endpoint Claude Code's `/usage` reads,
// called with the OAuth access token Claude Code keeps in
// ~/.claude/.credentials.json. One reply carries both limits. The token is
// only read, never refreshed or written (refreshing would race with Claude
// Code and could sign it out).

import Clutter from 'gi://Clutter';
import Gio from 'gi://Gio';
import GLib from 'gi://GLib';
import GObject from 'gi://GObject';
import Soup from 'gi://Soup';
import St from 'gi://St';

import {Extension} from 'resource:///org/gnome/shell/extensions/extension.js';
import * as Main from 'resource:///org/gnome/shell/ui/main.js';
import * as PanelMenu from 'resource:///org/gnome/shell/ui/panelMenu.js';
import * as PopupMenu from 'resource:///org/gnome/shell/ui/popupMenu.js';

import {
    barFraction, formatPercent, formatReset, formatUpdated, levelOf, parseLimits,
} from './usage.js';

const USAGE_URL = 'https://api.anthropic.com/api/oauth/usage';
// Background refresh. The endpoint is rate limited per account, measured at
// roughly 30 accepted requests per rolling hour, shared with Claude Code's
// own `/usage`; Claude Code itself rereads at most every 5 minutes. Polling
// every minute got about every second request refused (HTTP 429), so stay at
// 5 minutes and let opening the card fetch fresh numbers on demand.
const POLL_SECONDS = 300;
const BACKOFF_SECONDS = 120;        // after a 429: 2, 4, 8, then 10 minutes
const MAX_BACKOFF_SECONDS = 600;
const MIN_QUERY_GAP_MS = 10000;     // opening the card again this soon does not re-query
const STALE_AFTER_SECONDS = 900;    // flag the panel number once it is this old
const BAR_WIDTH = 300;
const DIM_OPACITY = 150;            // secondary text, works on light and dark themes

// Ring colours on the top bar, by level.
const RING_RGB = {
    none: [0.60, 0.60, 0.60],
    normal: [0.95, 0.95, 0.95],
    warning: [0.96, 0.76, 0.07],
    critical: [0.96, 0.38, 0.32],
};

const SIGN_IN_EXPIRED = 'Sign-in expired: open Claude Code to renew it';

function isCancelled(error) {
    return error instanceof GLib.Error &&
        error.matches(Gio.io_error_quark(), Gio.IOErrorEnum.CANCELLED);
}

const UsageIndicator = GObject.registerClass(
class UsageIndicator extends PanelMenu.Button {
    _init(extension) {
        super._init(0.5, 'Claude usage');

        this._credentials = Gio.File.new_for_path(
            GLib.build_filenamev([GLib.get_home_dir(), '.claude', '.credentials.json']));
        // The last successful reply, kept across restarts.
        this._cache = Gio.File.new_for_path(
            GLib.build_filenamev([GLib.get_user_cache_dir(), 'claude-usage', 'last.json']));
        this._limits = parseLimits({});
        this._lastOk = 0;       // seconds since epoch of the last successful query
        this._lastQuery = 0;    // ms since epoch of the last query attempt
        this._querying = false;
        this._error = null;
        this._rateLimited = 0;  // consecutive HTTP 429 replies
        this._timerId = 0;
        this._cancellable = new Gio.Cancellable();
        this._session = new Soup.Session({timeout: 20, user_agent: 'claude-usage-indicator/2.0'});

        // The robot is Anthropic's artwork and is not shipped with this
        // extension; install.sh copies it in from a local Claude Code editor
        // extension when there is one. Without it the indicator has no icon.
        const robotFile = extension.dir.get_child('clawd.svg');
        const robot = robotFile.query_exists(null) ? Gio.FileIcon.new(robotFile) : null;

        // -- top bar
        const box = new St.BoxLayout({style_class: 'claude-usage-panel'});
        if (robot) {
            box.add_child(new St.Icon({
                gicon: robot,
                style_class: 'claude-usage-panel-icon',
                y_align: Clutter.ActorAlign.CENTER,
            }));
        }
        this._ring = new St.DrawingArea({
            style_class: 'claude-usage-ring',
            y_align: Clutter.ActorAlign.CENTER,
        });
        this._ring.connect('repaint', area => this._drawRing(area));
        box.add_child(this._ring);
        this._label = new St.Label({y_align: Clutter.ActorAlign.CENTER});
        box.add_child(this._label);
        this.add_child(box);

        // -- popup card
        const card = new St.BoxLayout({vertical: true, style_class: 'claude-usage-card'});
        const header = new St.BoxLayout({style_class: 'claude-usage-header'});
        if (robot)
            header.add_child(new St.Icon({gicon: robot, icon_size: 24}));
        header.add_child(new St.Label({
            text: 'Weekly usage',
            style_class: 'claude-usage-title',
            x_expand: true,
            y_align: Clutter.ActorAlign.CENTER,
        }));
        this._updated = new St.Label({
            style_class: 'claude-usage-small',
            opacity: DIM_OPACITY,
            y_align: Clutter.ActorAlign.CENTER,
        });
        header.add_child(this._updated);
        card.add_child(header);

        this._rows = [this._addRow(card), this._addRow(card)];

        // Why the latest query failed, if it did. Deliberately quiet: the
        // numbers above stay, this only explains their age.
        this._note = new St.Label({
            style_class: 'claude-usage-small',
            opacity: DIM_OPACITY,
            visible: false,
        });
        this._note.clutter_text.line_wrap = true;
        card.add_child(this._note);
        this.menu.box.add_child(card);

        this.menu.addMenuItem(new PopupMenu.PopupSeparatorMenuItem());
        this._quitItem = new PopupMenu.PopupMenuItem('Quit');
        this._quitItem.connect('activate', () => this._quit(extension.uuid));
        this.menu.addMenuItem(this._quitItem);

        this.menu.connect('open-state-changed', (_menu, open) => {
            if (!open)
                return;
            this._growBars();
            this._queryNow();
        });

        this._render();
        this._loadCache();
        this._query();
    }

    _addRow(card) {
        const row = new St.BoxLayout({vertical: true, style_class: 'claude-usage-row'});
        const top = new St.BoxLayout();
        const name = new St.Label({style_class: 'claude-usage-name', x_expand: true});
        const percent = new St.Label({style_class: 'claude-usage-percent'});
        top.add_child(name);
        top.add_child(percent);
        const track = new St.Widget({
            style_class: 'claude-usage-track',
            width: BAR_WIDTH,
            x_align: Clutter.ActorAlign.START,
        });
        const fill = new St.Widget({style_class: 'claude-usage-fill'});
        track.add_child(fill);
        const reset = new St.Label({style_class: 'claude-usage-small', opacity: DIM_OPACITY});
        row.add_child(top);
        row.add_child(track);
        row.add_child(reset);
        card.add_child(row);
        return {name, percent, fill, reset, width: 0};
    }

    // Quit switches the extension off. It stays off, also across restarts,
    // until it is enabled again in the Extensions app or with
    // `gnome-extensions enable claude-usage@localhost`.
    _quit(uuid) {
        // Deferred: disabling destroys this menu, which must not happen while
        // the menu is still handling the click.
        this._quitId = GLib.idle_add(GLib.PRIORITY_DEFAULT, () => {
            this._quitId = 0;
            Main.extensionManager.disableExtension(uuid);
            return GLib.SOURCE_REMOVE;
        });
    }

    // -- the last successful reply, on disk

    _loadCache() {
        this._cache.load_contents_async(this._cancellable, (file, result) => {
            try {
                const [, contents] = file.load_contents_finish(result);
                if (this._cancellable.is_cancelled())
                    return;
                const saved = JSON.parse(new TextDecoder().decode(contents));
                const limits = parseLimits(saved.payload);
                // A query that already succeeded is newer than the file.
                if (this._lastOk || limits.weekly.percent === null ||
                    typeof saved.savedAt !== 'number')
                    return;
                this._limits = limits;
                this._lastOk = saved.savedAt;
                this._render();
            } catch (e) {
                // No file yet, or an unreadable one: start without it.
            }
        });
    }

    _saveCache(payload) {
        const text = JSON.stringify({
            savedAt: this._lastOk,
            payload: {limits: payload.limits ?? null, seven_day: payload.seven_day ?? null},
        });
        try {
            GLib.mkdir_with_parents(this._cache.get_parent().get_path(), 0o700);
        } catch (e) {
            return;
        }
        this._cache.replace_contents_bytes_async(
            new GLib.Bytes(new TextEncoder().encode(text)), null, false,
            Gio.FileCreateFlags.REPLACE_DESTINATION, null, (file, result) => {
                try {
                    file.replace_contents_finish(result);
                } catch (e) {
                    console.warn(`Claude Usage: could not save the last reply: ${e.message}`);
                }
            });
    }

    // -- querying (all async: nothing here may block the shell)

    /** Query right now, unless one is running or just ran. Used when the card opens. */
    _queryNow() {
        if (this._querying || Date.now() - this._lastQuery < MIN_QUERY_GAP_MS)
            return;
        this._query();
    }

    _query() {
        if (this._timerId) {
            GLib.source_remove(this._timerId);
            this._timerId = 0;
        }
        if (this._querying)
            return;
        this._querying = true;
        this._lastQuery = Date.now();
        this._render();     // shows "Updating…" on an open card
        this._credentials.load_contents_async(this._cancellable, (file, result) => {
            let token;
            try {
                const [, contents] = file.load_contents_finish(result);
                const oauth = JSON.parse(new TextDecoder().decode(contents)).claudeAiOauth;
                token = oauth.accessToken;
                if (!token)
                    throw new Error('no access token');
                if (oauth.expiresAt && oauth.expiresAt <= Date.now()) {
                    this._finish({error: SIGN_IN_EXPIRED});
                    return;
                }
            } catch (e) {
                if (!isCancelled(e))
                    this._finish({error: 'Not signed in: no Claude Code login found'});
                return;
            }
            this._request(token);
        });
    }

    _request(token) {
        const message = Soup.Message.new('GET', USAGE_URL);
        // Never follow a redirect: the token must only ever go to this host.
        message.set_flags(Soup.MessageFlags.NO_REDIRECT);
        message.request_headers.append('Authorization', `Bearer ${token}`);
        message.request_headers.append('anthropic-beta', 'oauth-2025-04-20');
        message.request_headers.append('Accept', 'application/json');
        this._session.send_and_read_async(message, GLib.PRIORITY_DEFAULT, this._cancellable,
            (session, result) => {
                // Whatever goes wrong in here, the next poll must still be
                // scheduled: an escaped exception would freeze the indicator.
                try {
                    this._onReply(session, result, message);
                } catch (e) {
                    if (!isCancelled(e)) {
                        console.error(e);
                        this._finish({error: 'Unexpected error reading usage'});
                    }
                }
            });
    }

    _onReply(session, result, message) {
        let bytes;
        try {
            bytes = session.send_and_read_finish(result);
        } catch (e) {
            if (!isCancelled(e))
                this._finish({error: "Offline: can't reach api.anthropic.com"});
            return;
        }
        // status_code, not get_status(): the latter throws for codes missing
        // from Soup's enum, and 429 is one of them.
        const status = message.status_code;
        if (status === 200) {
            let payload, limits;
            try {
                payload = JSON.parse(new TextDecoder().decode(bytes.get_data()));
                limits = parseLimits(payload);
            } catch {
                limits = null;
            }
            // A reply without the weekly number is not a successful query:
            // it must not replace good numbers with nothing.
            if (!limits || limits.weekly.percent === null)
                this._finish({error: 'Unexpected reply from the usage endpoint'});
            else
                this._finish({limits, payload});
        } else if (status === 401) {
            this._finish({error: SIGN_IN_EXPIRED});
        } else if (status === 429) {
            // Back off further on every consecutive refusal: 2, 4, 8, 10 min.
            this._rateLimited++;
            const retryAfter = Number(message.response_headers.get_one('Retry-After')) || 0;
            this._finish({
                error: 'Rate limited by Anthropic, retrying later',
                delay: Math.min(MAX_BACKOFF_SECONDS,
                    Math.max(BACKOFF_SECONDS * 2 ** (this._rateLimited - 1), retryAfter)),
            });
        } else {
            this._finish({error: `Usage request failed (HTTP ${status})`});
        }
    }

    _finish({limits = null, payload = null, error = null, delay = POLL_SECONDS}) {
        if (this._cancellable.is_cancelled())
            return;
        this._querying = false;
        if (limits) {
            this._limits = limits;
            this._lastOk = Date.now() / 1000;
            this._rateLimited = 0;
            this._saveCache(payload);
        }
        if (error && error !== this._error)
            console.warn(`Claude Usage: ${error}`);
        this._error = error;
        if (this._timerId)
            GLib.source_remove(this._timerId);
        this._timerId = GLib.timeout_add_seconds(GLib.PRIORITY_DEFAULT, delay, () => {
            this._timerId = 0;
            this._query();
            return GLib.SOURCE_REMOVE;
        });
        this._render();
    }

    // -- drawing

    _drawRing(area) {
        const cr = area.get_context();
        const [width, height] = area.get_surface_size();
        const {weekly} = this._limits;
        const line = width * 0.19;
        const radius = Math.min(width, height) / 2 - line / 2 - 0.5;
        cr.setLineWidth(line);
        cr.setSourceRGBA(1, 1, 1, 0.30);
        cr.arc(width / 2, height / 2, radius, 0, 2 * Math.PI);
        cr.stroke();
        const fraction = barFraction(weekly.percent);
        if (fraction > 0) {
            cr.setSourceRGBA(...RING_RGB[levelOf(weekly)], 1);
            const start = -Math.PI / 2;
            cr.arc(width / 2, height / 2, radius, start, start + 2 * Math.PI * fraction);
            cr.stroke();
        }
        cr.$dispose();
    }

    _render() {
        const {weekly, scoped} = this._limits;
        const now = new Date();
        const stale = this._lastOk > 0 && now.getTime() / 1000 - this._lastOk > STALE_AFTER_SECONDS;

        // An old number must not pass as live.
        this._label.text = formatPercent(weekly.percent) + (stale ? ' ⚠' : '');
        this._ring.queue_repaint();

        [weekly, scoped].forEach((limit, i) => {
            const row = this._rows[i];
            row.name.text = limit.label;
            row.percent.text = limit.percent === null ? 'no data' : formatPercent(limit.percent);
            row.reset.text = formatReset(limit.resetsAt, now);
            for (const level of ['normal', 'warning', 'critical'])
                row.fill.remove_style_class_name(level);
            row.fill.add_style_class_name(levelOf(limit));
            // Any usage shows at least a dot; none hides the fill.
            const fraction = barFraction(limit.percent);
            const width = fraction > 0 ? Math.max(8, Math.round(BAR_WIDTH * fraction)) : 0;
            row.fill.visible = width > 0;
            if (width === row.width)
                return;
            row.width = width;
            if (this.menu.isOpen) {
                // Glide to the new value, also when it lands mid-animation.
                row.fill.ease({
                    width,
                    duration: 300,
                    mode: Clutter.AnimationMode.EASE_OUT_QUAD,
                });
            } else {
                row.fill.remove_all_transitions();
                row.fill.width = width;
            }
        });

        if (this._querying && this.menu.isOpen)
            this._updated.text = 'Updating…';
        else if (this._lastOk)
            this._updated.text = formatUpdated(new Date(this._lastOk * 1000), now);
        else
            this._updated.text = this._querying ? 'Loading…' : 'No data yet';
        this._note.text = this._error ?? '';
        this._note.visible = this._error !== null;
    }

    _growBars() {
        for (const row of this._rows) {
            if (!row.width)
                continue;
            row.fill.remove_all_transitions();
            row.fill.width = 0;
            row.fill.ease({
                width: row.width,
                duration: 450,
                mode: Clutter.AnimationMode.EASE_OUT_CUBIC,
            });
        }
    }

    destroy() {
        this._cancellable.cancel();
        if (this._timerId) {
            GLib.source_remove(this._timerId);
            this._timerId = 0;
        }
        if (this._quitId) {
            GLib.source_remove(this._quitId);
            this._quitId = 0;
        }
        this._session.abort();
        super.destroy();
    }
});

export default class ClaudeUsageExtension extends Extension {
    enable() {
        this._indicator = new UsageIndicator(this);
        Main.panel.addToStatusArea(this.uuid, this._indicator);
    }

    disable() {
        this._indicator?.destroy();
        this._indicator = null;
    }
}
