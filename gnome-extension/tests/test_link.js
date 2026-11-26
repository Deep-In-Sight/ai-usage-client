// Tests of the usage panel extension's link to the agent: when it shows the
// agent's view and counts as attached (no shell, no bus).
//
// Run from anywhere:  gjs -m gnome-extension/tests/test_link.js

import {createLink, runEach} from '../ai-usage-panel@localhost/link.js';

let failures = 0;
let checks = 0;

function equal(actual, expected, what) {
    checks++;
    if (actual !== expected) {
        failures++;
        printerr(`FAIL ${what}: got ${JSON.stringify(actual)}, expected ${JSON.stringify(expected)}`);
    }
}

// attachNow is the token of the Attach a good view starts, after a check
// that a view may be shown at all.
function attachNow(link) {
    return link.mayShow() ? link.startAttach() : 0;
}

// -- the first appearance: a good view attaches once, Ping starts on the reply
{
    const link = createLink();
    equal(link.mayShow(), true, 'a new link may show');
    equal(link.attached, false, 'a new link is not attached');
    const g = link.appeared();
    equal(link.current(g), true, 'the appearance is current');
    const t = attachNow(link);
    equal(t !== 0, true, 'the first good view attaches');
    equal(attachNow(link), 0, 'no second Attach while one is pending');
    equal(link.attachDone(t, true), true, 'the reply attaches');
    equal(link.attached, true, 'attached');
    equal(attachNow(link), 0, 'no Attach while attached');
}

// -- I2: after a failure that is not a bad view, no later view shows or
//    attaches until the name vanishes and appears again
{
    const link = createLink();
    link.appeared();
    link.attachDone(link.startAttach(), true);
    link.failed(false);
    equal(link.attached, false, 'a failure detaches');
    equal(link.mayShow(), false, 'after a failure no view is shown');
    equal(link.startAttach(), 0, 'after a failure nothing attaches');
    for (let minute = 1; minute <= 3; minute++)
        equal(attachNow(link), 0, `the agent's redraw ${minute} min later does not attach`);
    link.vanished();
    equal(link.mayShow(), true, 'the name gone: the block ends');
    const g = link.appeared();
    equal(link.current(g), true, 'the name back');
    equal(attachNow(link) !== 0, true, 'the name back: the next good view attaches');
}

// -- I2: an Attach error is not a failure of the extension; the next good
//    view tries again
{
    const link = createLink();
    link.appeared();
    const t = link.startAttach();
    equal(link.attachDone(t, false), false, 'an Attach error does not attach');
    equal(link.attached, false, 'not attached after an Attach error');
    equal(attachNow(link) !== 0, true, 'the next view attaches again');
}

// -- I2: a bad view (the agent's answer is not a view) detaches, and the
//    next good view attaches again
{
    const link = createLink();
    link.appeared();
    link.attachDone(link.startAttach(), true);
    link.failed(true);
    equal(link.attached, false, 'a bad view detaches');
    equal(link.mayShow(), true, 'after a bad view the next good view is shown');
    equal(attachNow(link) !== 0, true, 'after a bad view the next good view attaches');
    // a bad view after a failure does not lift the block
    link.failed(false);
    link.failed(true);
    equal(link.mayShow(), false, 'a bad view after a failure keeps the block');
}

// -- I2: disable and enable make a new link: no block
{
    const old = createLink();
    old.appeared();
    old.failed(false);
    const link = createLink();
    link.appeared();
    equal(link.mayShow(), true, 'a new link after disable/enable shows');
}

// -- m1: an Attach reply that arrives after a failure, a vanish or a new
//    appearance does not attach
{
    const link = createLink();
    link.appeared();
    let t = link.startAttach();
    link.failed(true);
    equal(link.attachDone(t, true), false, 'a reply after a bad view does not attach');
    equal(link.attached, false, 'not attached by a late reply');
    const t2 = link.startAttach();
    equal(t2 !== 0 && t2 !== t, true, 'the next good view attaches with a new token');
    equal(link.attachDone(t, true), false, 'the old token stays dead');
    equal(link.attachDone(t2, true), true, 'the new reply attaches');

    t = createLink();
    t.appeared();
    const tok = t.startAttach();
    t.failed(false);
    equal(t.attachDone(tok, true), false, 'a reply after a failure does not attach');

    const v = createLink();
    v.appeared();
    const tv = v.startAttach();
    v.vanished();
    v.appeared();
    equal(v.attachDone(tv, true), false, 'a reply from the previous appearance does not attach');
    equal(v.attached, false, 'not attached by the previous appearance');
    equal(v.attachDone(0, true), false, 'token 0 never attaches');
}

// -- m2: replies made for an older appearance are not current
{
    const link = createLink();
    const g1 = link.appeared();
    const g2 = link.appeared(); // a new owner before the first proxy was ready
    equal(link.current(g1), false, 'the first appearance is old');
    equal(link.current(g2), true, 'the second is current');
    link.vanished();
    equal(link.current(g2), false, 'nothing is current after a vanish');
    link.close();
    equal(link.current(g2), false, 'nothing is current after close');
    equal(link.mayShow(), false, 'a closed link shows nothing');
    equal(link.startAttach(), 0, 'a closed link attaches nothing');
}

// -- runEach (disable()'s steps): each step runs whatever throws before
// it, and what throws is reported; the button's step is the last, so it
// goes whatever happened (review A-m4)
{
    const ran = [];
    const errors = [];
    runEach([
        () => ran.push('detach'),
        () => {
            throw new Error('the link');
        },
        () => ran.push('unwatch'),
        () => {
            throw new Error('the proxy');
        },
        () => ran.push('button'),
    ], e => errors.push(e.message));
    equal(ran.join(' '), 'detach unwatch button', 'every step runs');
    equal(errors.join(', '), 'the link, the proxy', 'each throw is reported');
    let last = false;
    runEach([() => {
        throw new Error('first');
    }, () => {
        last = true;
    }], null);
    equal(last, true, 'without a reporter the next step runs too');
}

print(`${checks - failures}/${checks} checks passed`);
if (failures)
    imports.system.exit(1);
