// Tests of the usage panel extension's view model (no shell, no bus).
//
// Run from anywhere:  gjs -m gnome-extension/tests/test_view.js
// It reads two files of the agent: the golden panel JSON (what GetView
// returns for the mock's values) and the fill table the Go test reads too.

import GLib from 'gi://GLib';

import {
    PROTOCOL, clickable, fillWidth, isDarkBackground, isEmpty, levelClass, parseView, useOrientation,
} from '../ai-usage-panel@localhost/view.js';

const here = GLib.path_get_dirname(GLib.filename_from_uri(import.meta.url)[0]);

function read(...parts) {
    const [, bytes] = GLib.file_get_contents(GLib.build_filenamev([here, '..', '..', ...parts]));
    return new TextDecoder().decode(bytes);
}

let failures = 0;
let checks = 0;

// canon is v with object keys sorted, so two views compare by content.
function canon(v) {
    if (Array.isArray(v))
        return v.map(canon);
    if (v !== null && typeof v === 'object')
        return Object.fromEntries(Object.keys(v).sort().map(k => [k, canon(v[k])]));
    return v;
}

function equal(actual, expected, what) {
    checks++;
    const a = JSON.stringify(canon(actual));
    const e = JSON.stringify(canon(expected));
    if (a !== e) {
        failures++;
        printerr(`FAIL ${what}:\n  got      ${a}\n  expected ${e}`);
    }
}

// withExtras is v with unknown keys added to every object, at every depth.
function withExtras(v) {
    if (Array.isArray(v))
        return v.map(withExtras);
    if (v !== null && typeof v === 'object') {
        const o = {};
        for (const k of Object.keys(v))
            o[k] = withExtras(v[k]);
        o.zz_future = {list: [1, {deep: null}], text: 'x'};
        o.icon_v2 = 7;
        return o;
    }
    return v;
}

const golden = read('agent', 'internal', 'tray', 'testdata', 'panel-combined.json');
const goldenView = JSON.parse(golden);

equal(PROTOCOL, 1, 'protocol');

// -- parseView
{
    // the agent's own view comes back as it is: every field, every type
    equal(parseView(golden), goldenView, 'the golden view');

    for (const bad of ['{', 'null', '[]', '42', '"x"', '', 'true'])
        equal(parseView(bad), null, `not a view: ${JSON.stringify(bad)}`);
    equal(parseView(undefined), null, 'no argument');
    equal(parseView({sections: []}), null, 'an object, not JSON');

    // RF1: a newer agent's extra fields are ignored at every level
    equal(parseView(JSON.stringify(withExtras(goldenView))), goldenView, 'unknown keys everywhere');

    const empty = {
        serial: 0, new_version: '', sections: [], prompt: '', server: '', actions: [], top: [],
    };
    equal(parseView('{}'), empty, 'an empty object');
    equal(parseView('{"sections": null, "actions": null, "top": null}'), empty, 'null lists');
    equal(parseView('{"sections": {"a": 1}, "actions": "x", "top": 3}'), empty, 'lists of the wrong type');
    equal(parseView('{"serial": "7", "server": 5, "prompt": null, "new_version": false}'), empty, 'scalars of the wrong type');

    // an older or broken agent's missing fields become defaults
    const v = parseView(JSON.stringify({
        serial: 3,
        sections: [
            1, null, 'x',
            {provider: 'claude', name: 'Claude', rows: [
                {id: 'r.claude.0', label: 'Weekly'},
                {id: 'r.claude.1', percent: 'x', level: 'severe', more: 'yes'},
                {id: 'r.claude.2', percent: 1e999},
                {id: 'r.claude.3', percent: 42.5, level: 'warning', percent_text: '43%'},
                null,
            ]},
            {provider: 'codex', rows: null, stale: 'true'},
        ],
        actions: [{id: 'quit', text: 'Quit'}, {id: 'refresh', text: 'Refreshing…', enabled: false}, 7],
        top: [{provider: 'claude', text: '42%', percent: 42, level: 'normal'}, {}],
    }));
    const row = (id, more = {}) => Object.assign({
        id, label: '', percent: null, percent_text: '', detail: '', level: 'none', more: false,
    }, more);
    equal(v.serial, 3, 'serial');
    equal(v.sections, [
        {provider: 'claude', name: 'Claude', account: '', updated: '', problem: '', stale: false, rows: [
            row('r.claude.0', {label: 'Weekly'}),
            row('r.claude.1'),
            row('r.claude.2'),
            row('r.claude.3', {percent: 42.5, level: 'warning', percent_text: '43%'}),
        ]},
        {provider: 'codex', name: '', account: '', updated: '', problem: '', stale: false, rows: []},
    ], 'sections with missing and wrong fields');
    equal(v.actions, [
        {id: 'quit', text: 'Quit', enabled: true, visible: true, checkbox: false, checked: false},
        {id: 'refresh', text: 'Refreshing…', enabled: false, visible: true, checkbox: false, checked: false},
    ], 'actions: missing flags show the action');
    equal(v.top, [
        {provider: 'claude', text: '42%', percent: 42, level: 'normal', stale: false},
        {provider: '', text: '', percent: null, level: 'none', stale: false},
    ], 'top');
    equal(parseView('{"sections": [{"rows": [{"percent": 50}]}]}').sections[0].rows[0].percent, 50, 'a number stays');
}

// -- isEmpty: the bridge's panel before the agent's first view
{
    equal(isEmpty(parseView('{}')), true, 'an empty object');
    // what Go's json.Marshal of an empty trayproto.Panel gives
    equal(isEmpty(parseView('{"serial":0,"new_version":"","sections":null,"prompt":"","server":"","actions":null,"top":null}')),
        true, 'an empty panel');
    equal(isEmpty(parseView(golden)), false, 'the golden view');
    equal(isEmpty(parseView('{"prompt": "Sign in to Claude Code or Codex"}')), false, 'a prompt alone');
    equal(isEmpty(parseView('{"sections": [{"provider": "claude"}], "actions": null}')), false, 'a section without actions');
    equal(isEmpty(parseView('{"actions": [{"id": "quit", "text": "Quit"}]}')), false, 'actions alone');
}

// -- fillWidth: the table the Go test reads (plan PR-18, pre-flight F12)
{
    const table = JSON.parse(read('agent', 'internal', 'panel', 'testdata', 'fillwidth.json'));
    checks++;
    if (!(table.cases.length >= 8)) {
        failures++;
        printerr('FAIL the fill table has too few cases');
    }
    for (const c of table.cases)
        equal(fillWidth(c.percent, table.track, table.bar), c.fill, `fillWidth(${c.percent})`);
    equal(fillWidth(1, 6, 8), 6, 'a track shorter than the bar is never overfilled');
    equal(fillWidth(-5, 300, 8), 0, 'negative');
    equal(fillWidth(undefined, 300, 8), 0, 'undefined');
    equal(fillWidth(NaN, 300, 8), 0, 'NaN');
    equal(fillWidth('50', 300, 8), 0, 'a string');
    equal(fillWidth(50, 0, 8), 0, 'no track');
}

// -- levelClass
{
    equal(levelClass('normal'), 'ai-usage-fill-normal', 'normal');
    equal(levelClass('warning'), 'ai-usage-fill-warning', 'warning');
    equal(levelClass('critical'), 'ai-usage-fill-critical', 'critical');
    for (const l of ['none', '', 'other', undefined, null])
        equal(levelClass(l), null, `level ${l}`);
}

// -- useOrientation (GNOME 48 deprecated `vertical`)
{
    for (const [major, want] of [[42, false], [46, false], [47, false], [48, true], [49, true], [50, true]])
        equal(useOrientation(major), want, `GNOME ${major}`);
}

// -- isDarkBackground (plan PR-16)
{
    equal(isDarkBackground(0x30, 0x30, 0x30), true, 'dark grey');
    equal(isDarkBackground(0xfa, 0xfa, 0xfa), false, 'near white');
    equal(isDarkBackground(0, 0, 0), true, 'black');
    equal(isDarkBackground(255, 255, 255), false, 'white');
}

// -- clickable (plan PR-15)
{
    const v = parseView(golden);
    equal(clickable(v, 'r.claude.0'), true, 'a row of the view');
    equal(clickable(v, 'r.codex.1'), true, 'another row');
    equal(clickable(v, 'r.codex.9'), false, 'a row the view does not have');
    equal(clickable(v, 'h.claude'), false, 'a header');
    equal(clickable(v, 'server'), false, 'the server line');
    equal(clickable(v, 'refresh'), true, 'an enabled action');
    equal(clickable(v, 'quit'), true, 'quit');
    equal(clickable(v, ''), false, 'no id');
    equal(clickable(v, undefined), false, 'undefined id');
    equal(clickable(null, 'r.claude.0'), false, 'no view');
    equal(clickable(v, 'newversion'), false, 'no new version');

    const changed = JSON.parse(golden);
    changed.new_version = 'New version available: 0.9.2';
    changed.actions[1] = {id: 'refresh', text: 'Refreshing…', enabled: false, visible: true, checkbox: false, checked: false};
    changed.actions[0].visible = false;
    changed.sections[1].rows.push({id: 'r.codex.4', label: 'More limits on the dashboard', percent: null,
        percent_text: '', detail: '', level: 'none', more: true});
    const w = parseView(JSON.stringify(changed));
    equal(clickable(w, 'newversion'), true, 'the new-version line');
    equal(clickable(w, 'refresh'), false, 'a disabled action');
    equal(clickable(w, 'dashboard'), false, 'a hidden action');
    equal(clickable(w, 'r.codex.4'), true, 'the More limits row');
}

print(`${checks - failures}/${checks} checks passed`);
if (failures)
    imports.system.exit(1);
