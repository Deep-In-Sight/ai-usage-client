'use strict';
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
// told (Detach), so it shows its tray icon instead; the extension stays
// away until the agent's bus name goes and comes back (link.js). Nothing
// else in the top bar is touched.
//
// This file is the source for GNOME 46 and 50; the agent's tools/mkext
// writes the GNOME 42 form from it, so it keeps to the import and export
// forms mkext knows.

const Clutter = imports.gi.Clutter;
const Gio = imports.gi.Gio;
const GLib = imports.gi.GLib;
const GObject = imports.gi.GObject;
const St = imports.gi.St;

const Config = imports.misc.config;
const Main = imports.ui.main;
const PanelMenu = imports.ui.panelMenu;
const PopupMenu = imports.ui.popupMenu;

const {createLink} = imports.misc.extensionUtils.getCurrentExtension().imports.link;
const {PROTOCOL, clickable, fillWidth, isDarkBackground, isEmpty, layoutKey, levelClass, menuUpdate, parseView, textOpacity, topEntries, useOrientation} = imports.misc.extensionUtils.getCurrentExtension().imports.view;

const BUS_NAME = 'local.AIUsage.Panel';
const OBJECT_PATH = '/local/AIUsage/Panel';
const PING_SECONDS = 30;
const BAR_WIDTH = 300;
const BAR_HEIGHT = 8;
const TOP_ICON_SIZE = 16;
const HEADER_ICON_SIZE = 20;

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

// A label of the small, dim text; its opacity is set by textOpacity.
function dimLabel(params = {}) {
    return new St.Label(Object.assign({style_class: 'ai-usage-small', y_align: Clutter.ActorAlign.CENTER}, params));
}

// steady: item (made with reactive and can_focus false) is not clickable
// and takes no keyboard focus, but is drawn in the menu's text colour. St
// marks a widget that is not reactive :insensitive and the shell's theme
// greys that out (and the popup-inactive-menu-item class with it); both
// are taken off again whenever St puts them back.
function steady(item) {
    const clear = () => {
        item.remove_style_pseudo_class('insensitive');
        item.remove_style_class_name('popup-inactive-menu-item');
    };
    clear();
    item.connect('notify::reactive', clear);
    return item;
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
        // the menu's items (layoutKey) and how each follows a new view
        this._aiuLayout = null;
        this._aiuUpdaters = [];
        // a view with other items, for when the open menu closes
        this._aiuPending = null;
        this._aiuPills = new St.BoxLayout({style_class: 'ai-usage-top'});
        this.add_child(this._aiuPills);
        this.menu.connect('open-state-changed', (_menu, open) => client.guard('menu', () => {
            if (open) {
                this._aiuFollowTheme();
            } else if (this._aiuPending !== null) {
                const view = this._aiuPending;
                this._aiuPending = null;
                this._aiuBuild(view);
            }
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
        for (const top of topEntries(view)) {
            const pill = new St.BoxLayout({style_class: 'ai-usage-pill'});
            pill.add_child(this._aiuPicture(top.provider, TOP_ICON_SIZE, top));
            if (top.text !== '')
                pill.add_child(new St.Label({text: top.text, y_align: Clutter.ActorAlign.CENTER}));
            this._aiuPills.add_child(pill);
        }

        switch (menuUpdate(this.menu.isOpen, this._aiuLayout, layoutKey(view))) {
        case 'in-place':
            this._aiuPending = null;
            for (const update of this._aiuUpdaters)
                update(view);
            break;
        case 'on-close':
            this._aiuPending = view;
            break;
        default:
            this._aiuBuild(view);
        }
    }

    // _aiuBuild makes the menu's items for view. Each item is made by a
    // function given pick (view -> the item's part of it) and keeps an
    // updater, so an open menu with the same items follows a new view in
    // place.
    _aiuBuild(view) {
        const menu = this.menu;
        menu.removeAll();
        this._aiuPending = null;
        this._aiuProblems = [];
        this._aiuUpdaters = [];
        this._aiuLayout = layoutKey(view);
        if (view.new_version !== '') {
            this._aiuAction(v => ({
                id: 'newversion', text: v.new_version, enabled: true, visible: true, checkbox: false, checked: false,
            }), view);
        }
        view.sections.forEach((s, i) => {
            if (i > 0)
                menu.addMenuItem(new PopupMenu.PopupSeparatorMenuItem());
            this._aiuSection(v => v.sections[i], view);
            s.rows.forEach((_r, j) => this._aiuRow(v => [v.sections[i].rows[j], v.sections[i].stale], view));
        });
        if (view.prompt !== '')
            this._aiuInfo(v => v.prompt, false, view);
        menu.addMenuItem(new PopupMenu.PopupSeparatorMenuItem());
        if (view.server !== '')
            this._aiuInfo(v => v.server, true, view);
        for (const a of view.actions) {
            if (a.visible)
                this._aiuAction(v => v.actions.find(b => b.id === a.id), view);
        }
    }

    // _aiuKeep adds item to the menu; update(part) draws pick(view) into
    // it, now and for each later view of the same items.
    _aiuKeep(item, pick, update, view) {
        update(pick(view));
        this._aiuUpdaters.push(v => update(pick(v)));
        this.menu.addMenuItem(item);
    }

    // The header: picture, bold name, dim account, right-aligned dim time;
    // the problem line under it. Not clickable.
    _aiuSection(pick, view) {
        const head = steady(new PopupMenu.PopupBaseMenuItem({reactive: false, can_focus: false}));
        const col = vbox({x_expand: true, style_class: 'ai-usage-section'});
        const line = new St.BoxLayout({style_class: 'ai-usage-header'});
        const picture = this._aiuPicture(pick(view).provider, HEADER_ICON_SIZE, PLAIN_RING);
        const name = new St.Label({style_class: 'ai-usage-name', y_align: Clutter.ActorAlign.CENTER});
        const account = dimLabel({x_expand: true});
        const updated = dimLabel();
        for (const a of [picture, name, account, updated])
            line.add_child(a);
        col.add_child(line);
        head.add_child(col);
        let problem = null;
        this._aiuKeep(head, pick, s => {
            name.text = s.name;
            account.text = s.account;
            updated.text = s.updated;
            picture.opacity = name.opacity = textOpacity(false, s.stale);
            account.opacity = updated.opacity = textOpacity(true, s.stale);
            if (s.problem !== '' && problem === null) {
                problem = new St.Label({style_class: 'ai-usage-problem'});
                problem.clutter_text.line_wrap = true;
                if (!this._aiuDark)
                    problem.add_style_class_name('ai-usage-on-light');
                this._aiuProblems.push(problem);
                col.add_child(problem);
            } else if (s.problem === '' && problem !== null) {
                this._aiuProblems = this._aiuProblems.filter(p => p !== problem);
                problem.destroy();
                problem = null;
            }
            if (problem !== null) {
                problem.text = s.problem;
                problem.opacity = textOpacity(false, s.stale);
            }
        }, view);
    }

    _aiuRow(pick, view) {
        const [first] = pick(view);
        const item = new PopupMenu.PopupBaseMenuItem();
        const col = vbox({x_expand: true, style_class: 'ai-usage-row'});
        const line = new St.BoxLayout();
        const label = new St.Label({style_class: 'ai-usage-label', x_expand: true});
        line.add_child(label);
        col.add_child(line);
        // the More row is its label only
        let percent = null, track = null, detail = null;
        if (!first.more) {
            percent = new St.Label({style_class: 'ai-usage-percent'});
            line.add_child(percent);
            track = new St.Widget({
                style_class: 'ai-usage-track', width: BAR_WIDTH, height: BAR_HEIGHT, x_align: Clutter.ActorAlign.START,
            });
            col.add_child(track);
            detail = dimLabel();
            col.add_child(detail);
        }
        item.add_child(col);
        this._aiuKeep(item, pick, ([r, stale]) => {
            label.text = r.label;
            label.opacity = textOpacity(false, stale);
            if (percent === null)
                return;
            percent.text = r.percent_text;
            percent.opacity = textOpacity(false, stale);
            if (r.level === 'critical')
                percent.add_style_class_name('ai-usage-critical');
            else
                percent.remove_style_class_name('ai-usage-critical');
            track.destroy_all_children();
            const width = fillWidth(r.percent, BAR_WIDTH, BAR_HEIGHT);
            const level = levelClass(r.level);
            if (width > 0 && level !== null)
                track.add_child(new St.Widget({style_class: `ai-usage-fill ${level}`, width, height: BAR_HEIGHT}));
            track.opacity = textOpacity(false, stale);
            detail.text = r.detail;
            detail.opacity = textOpacity(true, stale);
        }, view);
        item.connect('activate', () => this._aiuClient.guard('click', () => this._aiuClient.click(first.id)));
    }

    // A line of text that is not clickable: the sign-in prompt, the dim
    // server line.
    _aiuInfo(pick, dim, view) {
        const item = steady(new PopupMenu.PopupMenuItem('', {reactive: false, can_focus: false}));
        if (dim)
            item.label.add_style_class_name('ai-usage-small');
        item.label.opacity = textOpacity(dim, false);
        this._aiuKeep(item, pick, text => {
            item.label.text = text;
        }, view);
    }

    _aiuAction(pick, view) {
        const item = new PopupMenu.PopupMenuItem('');
        const id = pick(view).id;
        this._aiuKeep(item, pick, a => {
            item.label.text = a.text;
            item.setSensitive(a.enabled);
            if (a.checkbox)
                item.setOrnament(a.checked ? PopupMenu.Ornament.CHECK : PopupMenu.Ornament.NONE);
        }, view);
        item.connect('activate', () => this._aiuClient.guard('click', () => this._aiuClient.click(id)));
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
// view, attaches, pings and sends clicks. When it shows a view and counts
// as attached is link.js's (tested there).
class PanelClient {
    constructor(ext) {
        this._uuid = ext.uuid;
        this._link = createLink();
        this._proxy = null;
        this._signalId = 0;
        this._pingId = 0;
        this._watchId = 0;
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
    // hidden and the agent told, and the shell carries on. The extension
    // stays away until the agent's name goes and comes back (link.js).
    guard(what, fn) {
        try {
            return fn();
        } catch (e) {
            console.error(`${this._uuid}: ${what}:`, e);
            this._failed(false);
            return undefined;
        }
    }

    // _failed: hide, stop pinging, tell the agent. badView: the agent's
    // answer was no view; its next good view is shown and attached again.
    _failed(badView) {
        try {
            this._link.failed(badView);
            this._setShown(false);
            this._stopPing();
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
        const generation = this._link.appeared();
        // DO_NOT_AUTO_START: no call ever asks the bus to start a program
        // for the name; only a running agent answers.
        new PanelProxy(Gio.DBus.session, BUS_NAME, OBJECT_PATH, (proxy, error) => this.guard('proxy', () => {
            // a later appearance, a vanish or disable() came first
            if (!this._button || !this._link.current(generation))
                return;
            if (error) {
                console.log(`${this._uuid}: no connection to the agent: ${error.message}`);
                return;
            }
            this._proxy = proxy;
            this._signalId = proxy.connectSignal('ViewChanged', () => this.guard('ViewChanged', () => this._refresh()));
            this._refresh();
        }), null, Gio.DBusProxyFlags.DO_NOT_AUTO_START);
    }

    _vanished() {
        this._link.vanished();
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
    }

    _refresh() {
        const proxy = this._proxy;
        if (!proxy || !this._link.mayShow())
            return;
        const generation = this._link.generation;
        proxy.GetViewRemote((result, error) => this.guard('GetView', () => {
            if (!this._link.current(generation) || !this._link.mayShow())
                return;
            if (error) {
                // No answer (a hung agent): keep the last view, if any.
                console.log(`${this._uuid}: GetView failed: ${error.message}`);
                if (this._view === null)
                    this._setShown(false);
                return;
            }
            const view = parseView(result[0]);
            if (view === null) {
                console.error(`${this._uuid}: GetView did not return a view`);
                this._failed(true);
                return;
            }
            if (isEmpty(view)) {
                // The agent has no panel yet: wait for its ViewChanged.
                if (this._view === null)
                    this._setShown(false);
                return;
            }
            this._button.render(view);
            this._view = view;
            this._setShown(true);
            const token = this._link.startAttach();
            if (token !== 0)
                this._attach(proxy, token);
        }));
    }

    _attach(proxy, token) {
        proxy.AttachRemote(PROTOCOL, Config.PACKAGE_VERSION, (result, error) => this.guard('Attach', () => {
            if (error)
                console.log(`${this._uuid}: Attach failed: ${error.message}`);
            // false for an error and for a reply that came after a failure,
            // a vanish or disable()
            if (!this._link.attachDone(token, !error))
                return;
            if (this._pingId === 0) {
                this._pingId = GLib.timeout_add_seconds(GLib.PRIORITY_DEFAULT, PING_SECONDS, () => {
                    this.guard('Ping', () => {
                        if (this._proxy && this._link.attached)
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
        if (this._proxy && this._link.mayShow() && clickable(this._view, id))
            this._proxy.ClickRemote(id, () => {});
    }

    destroy() {
        try {
            this._detach();
        } catch (e) {
            // the agent may already be gone
        }
        this._link.close();
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

class AIUsagePanelExtension {
    constructor(meta) {
        this.uuid = meta.uuid;
        this.path = meta.path;
        this.dir = meta.dir;
        this.metadata = meta.metadata;
    }

    enable() {
        this._client = new PanelClient(this);
    }

    disable() {
        this._client?.destroy();
        this._client = null;
    }
}

function init(meta) {
    return new AIUsagePanelExtension(meta);
}
