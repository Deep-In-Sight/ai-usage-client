// AI Usage Panel: the AI Usage agent's usage panel in the GNOME top bar.
//
// Display only. Everything shown comes finished from the AI Usage agent on
// this PC over the session bus (local.AIUsage.Panel): this extension reads
// no file but its own (clawd.svg; the shell reads stylesheet.css), uses no
// network and starts no program. A click sends the clicked item's id and
// nothing else.
//
// While the agent is not there (not running, removed) the button is
// hidden. When anything here fails, the button is hidden and the agent is
// told (Detach), so it shows its tray icon instead. Nothing else in the top
// bar is touched.
//
// This file is the source for GNOME 46 and 50; the agent's tools/mkext
// writes the GNOME 42 form from it, so it keeps to the import and export
// forms mkext knows.

import Clutter from 'gi://Clutter';
import Gio from 'gi://Gio';
import GLib from 'gi://GLib';
import GObject from 'gi://GObject';
import St from 'gi://St';

import {Extension} from 'resource:///org/gnome/shell/extensions/extension.js';
import * as Config from 'resource:///org/gnome/shell/misc/config.js';
import * as Main from 'resource:///org/gnome/shell/ui/main.js';
import * as PanelMenu from 'resource:///org/gnome/shell/ui/panelMenu.js';
import * as PopupMenu from 'resource:///org/gnome/shell/ui/popupMenu.js';

import {PROTOCOL, clickable, fillWidth, isDarkBackground, isEmpty, levelClass, parseView, useOrientation} from './view.js';

const BUS_NAME = 'local.AIUsage.Panel';
const OBJECT_PATH = '/local/AIUsage/Panel';
const PING_SECONDS = 30;
const BAR_WIDTH = 300;
const BAR_HEIGHT = 8;
const TOP_ICON_SIZE = 16;
const HEADER_ICON_SIZE = 20;
const DIM_OPACITY = 150;    // secondary text, as in the original extension
const STALE_OPACITY = 153;  // a group whose numbers are old: 0.6

// The ring's colour by level: the bars' colours, grey without a number.
const RING_RGB = {
    none: [0x99 / 255, 0x99 / 255, 0x99 / 255],
    normal: [0xd9 / 255, 0x77 / 255, 0x57 / 255],
    warning: [0xf5 / 255, 0xc2 / 255, 0x11 / 255],
    critical: [0xf6 / 255, 0x61 / 255, 0x51 / 255],
};

// The header's picture for a provider without artwork: a whole ring.
const PLAIN_RING = {level: 'normal', percent: 100, stale: false};

const PanelIface = `<node>
  <interface name="local.AIUsage.Panel1">
    <property name="Protocol" type="u" access="read"/>
    <method name="Attach">
      <arg type="u" direction="in" name="extension_protocol"/>
      <arg type="s" direction="in" name="shell_version"/>
      <arg type="u" direction="out" name="protocol"/>
    </method>
    <method name="Detach"/>
    <method name="GetView">
      <arg type="s" direction="out" name="view_json"/>
    </method>
    <method name="Click">
      <arg type="s" direction="in" name="id"/>
    </method>
    <method name="Ping"/>
    <signal name="ViewChanged">
      <arg type="t" name="serial"/>
    </signal>
  </interface>
</node>`;
const PanelProxy = Gio.DBusProxy.makeProxyWrapper(PanelIface);

const SHELL_MAJOR = parseInt(Config.PACKAGE_VERSION.split('.')[0], 10);

// vbox is a vertical St.BoxLayout on every supported shell (GNOME 48
// deprecated `vertical`).
function vbox(params) {
    const p = Object.assign({}, params);
    if (useOrientation(SHELL_MAJOR))
        p.orientation = Clutter.Orientation.VERTICAL;
    else
        p.vertical = true;
    return new St.BoxLayout(p);
}

function dimLabel(text, params = {}) {
    return new St.Label(Object.assign({
        text, style_class: 'ai-usage-small', opacity: DIM_OPACITY, y_align: Clutter.ActorAlign.CENTER,
    }, params));
}

// The button in the top bar and its menu: draws a view, nothing more.
// client.guard runs every callback; client.click sends a click.
const Indicator = GObject.registerClass(
class AIUsagePanelIndicator extends PanelMenu.Button {
    _init(client, clawd) {
        super._init(0.5, 'AI Usage Panel', false);
        this._aiuClient = client;
        this._aiuClawd = clawd;
        this._aiuProblems = [];
        this._aiuDark = true;
        this._aiuPills = new St.BoxLayout({style_class: 'ai-usage-top'});
        this.add_child(this._aiuPills);
        this.menu.connect('open-state-changed', (_menu, open) => client.guard('menu', () => {
            if (open)
                this._aiuFollowTheme();
        }));
    }

    // A ring of size px, coloured by level; filled to percent when there
    // is one (grey and whole without).
    _aiuRing(size, top) {
        const ring = new St.DrawingArea({width: size, height: size, y_align: Clutter.ActorAlign.CENTER});
        ring.connect('repaint', area => this._aiuClient.guard('ring', () => {
            const cr = area.get_context();
            try {
                const [w, h] = area.get_surface_size();
                const line = Math.max(2, Math.round(w * 0.19));
                const radius = Math.min(w, h) / 2 - line / 2 - 0.5;
                const rgb = RING_RGB[top.level] ?? RING_RGB.none;
                cr.setLineWidth(line);
                if (top.level === 'none' || top.percent === null) {
                    cr.setSourceRGBA(...RING_RGB.none, 1);
                    cr.arc(w / 2, h / 2, radius, 0, 2 * Math.PI);
                    cr.stroke();
                    return;
                }
                cr.setSourceRGBA(0.5, 0.5, 0.5, 0.45);
                cr.arc(w / 2, h / 2, radius, 0, 2 * Math.PI);
                cr.stroke();
                const fraction = Math.min(Math.max(top.percent, 0), 100) / 100;
                if (fraction > 0) {
                    const start = -Math.PI / 2;
                    cr.setSourceRGBA(...rgb, top.stale ? 0.5 : 1);
                    cr.arc(w / 2, h / 2, radius, start, start + 2 * Math.PI * fraction);
                    cr.stroke();
                }
            } finally {
                cr.$dispose();
            }
        }));
        return ring;
    }

    _aiuPicture(provider, size, top) {
        if (provider === 'claude')
            return new St.Icon({gicon: this._aiuClawd, icon_size: size, y_align: Clutter.ActorAlign.CENTER});
        return this._aiuRing(size, top);
    }

    render(view) {
        this._aiuPills.destroy_all_children();
        for (const top of view.top) {
            const pill = new St.BoxLayout({style_class: 'ai-usage-pill'});
            pill.add_child(this._aiuPicture(top.provider, TOP_ICON_SIZE, top));
            pill.add_child(new St.Label({text: top.text, y_align: Clutter.ActorAlign.CENTER}));
            this._aiuPills.add_child(pill);
        }

        const menu = this.menu;
        menu.removeAll();
        this._aiuProblems = [];
        if (view.new_version !== '')
            this._aiuAction({id: 'newversion', text: view.new_version, enabled: true, visible: true, checkbox: false});
        view.sections.forEach((s, i) => {
            if (i > 0)
                menu.addMenuItem(new PopupMenu.PopupSeparatorMenuItem());
            this._aiuSection(s);
        });
        if (view.prompt !== '')
            menu.addMenuItem(new PopupMenu.PopupMenuItem(view.prompt, {reactive: false, can_focus: false}));
        menu.addMenuItem(new PopupMenu.PopupSeparatorMenuItem());
        if (view.server !== '') {
            const server = new PopupMenu.PopupMenuItem(view.server, {reactive: false, can_focus: false});
            server.label.add_style_class_name('ai-usage-small');
            server.label.opacity = DIM_OPACITY;
            menu.addMenuItem(server);
        }
        for (const a of view.actions) {
            if (a.visible)
                this._aiuAction(a);
        }
    }

    _aiuSection(s) {
        const head = new PopupMenu.PopupBaseMenuItem({reactive: false, can_focus: false});
        // not clickable, but drawn in the menu's text colour, not greyed out
        head.remove_style_class_name('popup-inactive-menu-item');
        const col = vbox({x_expand: true, style_class: 'ai-usage-section'});
        const line = new St.BoxLayout({style_class: 'ai-usage-header'});
        line.add_child(this._aiuPicture(s.provider, HEADER_ICON_SIZE, PLAIN_RING));
        line.add_child(new St.Label({text: s.name, style_class: 'ai-usage-name', y_align: Clutter.ActorAlign.CENTER}));
        line.add_child(dimLabel(s.account, {x_expand: true}));
        line.add_child(dimLabel(s.updated));
        col.add_child(line);
        if (s.problem !== '') {
            const problem = new St.Label({text: s.problem, style_class: 'ai-usage-problem'});
            problem.clutter_text.line_wrap = true;
            if (!this._aiuDark)
                problem.add_style_class_name('ai-usage-on-light');
            this._aiuProblems.push(problem);
            col.add_child(problem);
        }
        head.add_child(col);
        if (s.stale)
            head.opacity = STALE_OPACITY;
        this.menu.addMenuItem(head);
        for (const r of s.rows)
            this._aiuRow(r, s.stale);
    }

    _aiuRow(r, stale) {
        const item = new PopupMenu.PopupBaseMenuItem();
        const col = vbox({x_expand: true, style_class: 'ai-usage-row'});
        const line = new St.BoxLayout();
        line.add_child(new St.Label({text: r.label, style_class: 'ai-usage-label', x_expand: true}));
        if (!r.more) {
            const percent = new St.Label({text: r.percent_text, style_class: 'ai-usage-percent'});
            if (r.level === 'critical')
                percent.add_style_class_name('ai-usage-critical');
            line.add_child(percent);
        }
        col.add_child(line);
        if (!r.more) {
            const track = new St.Widget({
                style_class: 'ai-usage-track', width: BAR_WIDTH, height: BAR_HEIGHT, x_align: Clutter.ActorAlign.START,
            });
            const width = fillWidth(r.percent, BAR_WIDTH, BAR_HEIGHT);
            const level = levelClass(r.level);
            if (width > 0 && level !== null)
                track.add_child(new St.Widget({style_class: `ai-usage-fill ${level}`, width, height: BAR_HEIGHT}));
            col.add_child(track);
            col.add_child(dimLabel(r.detail));
        }
        item.add_child(col);
        if (stale)
            item.opacity = STALE_OPACITY;
        item.connect('activate', () => this._aiuClient.guard('click', () => this._aiuClient.click(r.id)));
        this.menu.addMenuItem(item);
    }

    _aiuAction(a) {
        const item = new PopupMenu.PopupMenuItem(a.text);
        if (!a.enabled)
            item.setSensitive(false);
        if (a.checkbox)
            item.setOrnament(a.checked ? PopupMenu.Ornament.CHECK : PopupMenu.Ornament.NONE);
        item.connect('activate', () => this._aiuClient.guard('click', () => this._aiuClient.click(a.id)));
        this.menu.addMenuItem(item);
    }

    // The problem line's colour follows the menu's background (plan PR-16):
    // the first styled background up from the menu's box.
    _aiuFollowTheme() {
        let dark = true;
        for (let a = this.menu.box; a; a = a.get_parent()) {
            const node = a instanceof St.Widget ? a.peek_theme_node() : null;
            if (!node)
                continue;
            const c = node.get_background_color();
            if (c.alpha > 0) {
                dark = isDarkBackground(c.red, c.green, c.blue);
                break;
            }
        }
        this._aiuDark = dark;
        for (const p of this._aiuProblems) {
            if (dark)
                p.remove_style_class_name('ai-usage-on-light');
            else
                p.add_style_class_name('ai-usage-on-light');
        }
    }
});

// The connection to the agent: watches its bus name, reads and draws its
// view, attaches, pings and sends clicks.
class PanelClient {
    constructor(ext) {
        this._uuid = ext.uuid;
        this._proxy = null;
        this._signalId = 0;
        this._pingId = 0;
        this._watchId = 0;
        this._attached = false;
        this._view = null;
        this._button = null;
        this.guard('enable', () => {
            // Clawd, the Claude Code mascot: (c) Anthropic PBC, see the README.
            const clawd = Gio.FileIcon.new(ext.dir.get_child('clawd.svg'));
            this._button = new Indicator(this, clawd);
            Main.panel.addToStatusArea(this._uuid, this._button);
            this._setShown(false);
            this._watchId = Gio.bus_watch_name(Gio.BusType.SESSION, BUS_NAME, Gio.BusNameWatcherFlags.NONE,
                () => this.guard('appeared', () => this._appeared()),
                () => this.guard('vanished', () => this._vanished()));
        });
    }

    // guard runs fn; when it throws, the error is logged, the button
    // hidden and the agent told, and the shell carries on.
    guard(what, fn) {
        try {
            return fn();
        } catch (e) {
            console.error(`${this._uuid}: ${what}:`, e);
            this._failed();
            return undefined;
        }
    }

    _failed() {
        try {
            this._setShown(false);
            this._stopPing();
            this._attached = false;
            this._detach();
        } catch (e) {
            console.error(`${this._uuid}: after a failure:`, e);
        }
    }

    _detach() {
        if (this._proxy) {
            this._proxy.DetachRemote(() => {});
        }
    }

    _setShown(shown) {
        if (!this._button)
            return;
        if (!shown && this._button.menu.isOpen)
            this._button.menu.close();
        this._button.visible = shown;
        if (this._button.container)
            this._button.container.visible = shown;
    }

    _appeared() {
        this._dropProxy();
        this._view = null;
        new PanelProxy(Gio.DBus.session, BUS_NAME, OBJECT_PATH, (proxy, error) => this.guard('proxy', () => {
            if (!this._button || this._watchId === 0)
                return;
            if (error) {
                console.log(`${this._uuid}: no connection to the agent: ${error.message}`);
                return;
            }
            this._proxy = proxy;
            this._signalId = proxy.connectSignal('ViewChanged', () => this.guard('ViewChanged', () => this._refresh()));
            this._refresh();
        }));
    }

    _vanished() {
        this._dropProxy();
        this._view = null;
        this._setShown(false);
    }

    _dropProxy() {
        this._stopPing();
        if (this._proxy && this._signalId)
            this._proxy.disconnectSignal(this._signalId);
        this._signalId = 0;
        this._proxy = null;
        this._attached = false;
    }

    _refresh() {
        const proxy = this._proxy;
        if (!proxy)
            return;
        proxy.GetViewRemote((result, error) => this.guard('GetView', () => {
            if (proxy !== this._proxy)
                return;
            if (error) {
                // No answer (a hung agent): keep the last view, if any.
                console.log(`${this._uuid}: GetView failed: ${error.message}`);
                if (this._view === null)
                    this._setShown(false);
                return;
            }
            const view = parseView(result[0]);
            if (view === null)
                throw new Error('GetView did not return a view');
            if (isEmpty(view)) {
                // The agent has no panel yet: wait for its ViewChanged.
                if (this._view === null)
                    this._setShown(false);
                return;
            }
            this._button.render(view);
            this._view = view;
            this._setShown(true);
            if (!this._attached)
                this._attach(proxy);
        }));
    }

    _attach(proxy) {
        proxy.AttachRemote(PROTOCOL, Config.PACKAGE_VERSION, (result, error) => this.guard('Attach', () => {
            if (proxy !== this._proxy)
                return;
            if (error) {
                console.log(`${this._uuid}: Attach failed: ${error.message}`);
                return;
            }
            this._attached = true;
            if (this._pingId === 0) {
                this._pingId = GLib.timeout_add_seconds(GLib.PRIORITY_DEFAULT, PING_SECONDS, () => {
                    this.guard('Ping', () => {
                        if (this._proxy)
                            this._proxy.PingRemote(() => {});
                    });
                    return this._pingId !== 0 ? GLib.SOURCE_CONTINUE : GLib.SOURCE_REMOVE;
                });
            }
        }));
    }

    _stopPing() {
        if (this._pingId !== 0) {
            GLib.source_remove(this._pingId);
            this._pingId = 0;
        }
    }

    click(id) {
        if (this._proxy && clickable(this._view, id))
            this._proxy.ClickRemote(id, () => {});
    }

    destroy() {
        try {
            this._detach();
        } catch (e) {
            // the agent may already be gone
        }
        if (this._watchId !== 0) {
            Gio.bus_unwatch_name(this._watchId);
            this._watchId = 0;
        }
        this._dropProxy();
        this._view = null;
        if (this._button) {
            this._button.destroy();
            this._button = null;
        }
    }
}

export default class AIUsagePanelExtension extends Extension {
    enable() {
        this._client = new PanelClient(this);
    }

    disable() {
        this._client?.destroy();
        this._client = null;
    }
}
