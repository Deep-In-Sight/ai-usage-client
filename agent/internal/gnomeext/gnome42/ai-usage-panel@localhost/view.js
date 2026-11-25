// The usage panel's view, as the AI Usage agent sends it over the session
// bus (GetView): reading it and the few decisions drawn from it. Plain
// JavaScript without any Shell or gi:// import, so plain `gjs -m` tests it
// (gnome-extension/tests/test_view.js).
//
// The agent and this extension are updated at different times (the shell
// keeps running the old extension until the next login), so a view is read
// leniently: unknown keys are dropped, a missing or wrongly typed value
// becomes its default, and nothing here throws.

// The protocol this extension speaks (Attach's first argument).
var PROTOCOL = 1;

const LEVELS = ['none', 'normal', 'warning', 'critical'];

function isObject(v) {
    return v !== null && typeof v === 'object' && !Array.isArray(v);
}

function text(v) {
    return typeof v === 'string' ? v : '';
}

function flag(v) {
    return v === true;
}

function number(v) {
    return typeof v === 'number' && Number.isFinite(v) ? v : null;
}

function level(v) {
    return LEVELS.includes(v) ? v : 'none';
}

// The objects of a list (anything else in it is dropped), each read by read.
function objects(v, read) {
    return Array.isArray(v) ? v.filter(isObject).map(read) : [];
}

function readRow(r) {
    return {
        id: text(r.id),
        label: text(r.label),
        percent: number(r.percent),
        percent_text: text(r.percent_text),
        detail: text(r.detail),
        level: level(r.level),
        more: flag(r.more),
    };
}

function readSection(s) {
    return {
        provider: text(s.provider),
        name: text(s.name),
        account: text(s.account),
        updated: text(s.updated),
        problem: text(s.problem),
        stale: flag(s.stale),
        rows: objects(s.rows, readRow),
    };
}

// An action without its enabled or visible flag is shown and usable: the
// agent always sends both, and hiding Quit would be the worse mistake.
function readAction(a) {
    return {
        id: text(a.id),
        text: text(a.text),
        enabled: a.enabled !== false,
        visible: a.visible !== false,
        checkbox: flag(a.checkbox),
        checked: flag(a.checked),
    };
}

function readTop(t) {
    return {
        provider: text(t.provider),
        text: text(t.text),
        percent: number(t.percent),
        level: level(t.level),
        stale: flag(t.stale),
    };
}

// parseView reads GetView's JSON into a view with every field present, or
// null when it is not a JSON object.
function parseView(json) {
    if (typeof json !== 'string')
        return null;
    let v;
    try {
        v = JSON.parse(json);
    } catch (e) {
        return null;
    }
    if (!isObject(v))
        return null;
    return {
        serial: number(v.serial) ?? 0,
        new_version: text(v.new_version),
        sections: objects(v.sections, readSection),
        prompt: text(v.prompt),
        server: text(v.server),
        actions: objects(v.actions, readAction),
        top: objects(v.top, readTop),
    };
}

// isEmpty: view has nothing to draw: no section, prompt, server line,
// action or top-bar number. The agent's bridge answers such a panel before
// the agent's first view; the extension waits for ViewChanged instead of
// showing it and attaching.
function isEmpty(view) {
    return view.sections.length === 0 && view.prompt === '' && view.server === '' &&
        view.new_version === '' && view.actions.length === 0 && view.top.length === 0;
}

// fillWidth is how much of a track of width track the bar fills for the
// shown percent (the agent's panel.FillWidth, one table for both:
// agent/internal/panel/testdata/fillwidth.json): none for no value or 0 %,
// the whole track from 100 %, otherwise the share of the track but at
// least a round dot (barHeight). percent is rounded first, as the number
// next to the bar is.
function fillWidth(percent, track, barHeight) {
    if (typeof percent !== 'number' || Number.isNaN(percent) || !(track > 0))
        return 0;
    const p = Math.floor(percent + 0.5);
    if (p <= 0)
        return 0;
    if (p >= 100)
        return track;
    const w = Math.floor(track * p / 100 + 0.5);
    return Math.min(Math.max(w, barHeight), track);
}

// levelClass is the fill's style class for a level, null for none (no
// value: no fill).
function levelClass(lvl) {
    switch (lvl) {
    case 'normal':
    case 'warning':
    case 'critical':
        return `ai-usage-fill-${lvl}`;
    default:
        return null;
    }
}

// useOrientation: GNOME 48 marked St.BoxLayout's `vertical` deprecated in
// favour of `orientation`.
function useOrientation(shellMajor) {
    return shellMajor >= 48;
}

function linear(c) {
    const s = c / 255;
    return s <= 0.04045 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
}

// isDarkBackground: the relative luminance (sRGB, 0..255 a channel) is
// below 0.5. The problem line's colour follows it.
function isDarkBackground(r, g, b) {
    return 0.2126 * linear(r) + 0.7152 * linear(g) + 0.0722 * linear(b) < 0.5;
}

// clickable: id is a row of view (the "More limits" row too), an action
// that is visible and enabled, or the new-version line while there is one.
// The agent checks the same again; this only keeps dead clicks local.
function clickable(view, id) {
    if (!isObject(view) || typeof id !== 'string' || id === '')
        return false;
    if (id === 'newversion')
        return text(view.new_version) !== '';
    for (const s of Array.isArray(view.sections) ? view.sections : []) {
        if (isObject(s) && Array.isArray(s.rows) && s.rows.some(r => isObject(r) && r.id === id))
            return true;
    }
    return (Array.isArray(view.actions) ? view.actions : []).some(a =>
        isObject(a) && a.id === id && a.visible !== false && a.enabled !== false);
}
