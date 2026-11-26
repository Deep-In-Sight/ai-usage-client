'use strict';
// The extension's link to the AI Usage agent: when it may show the agent's
// view and when it counts as attached. Plain JavaScript without any Shell
// or gi:// import, so plain `gjs -m` tests it
// (gnome-extension/tests/test_link.js); extension.js does the bus calls.
//
// A failure of the extension hides the button and tells the agent
// (Detach), so the agent shows its tray icons instead (design 6.7). The
// extension then stays away until the agent's bus name goes and comes back,
// or the extension is disabled and enabled again (a new link): attaching
// again on the agent's next view would make the panel and the tray icons
// swap every minute while the failure repeats. Only a bad view (GetView's
// answer is no view: the agent's fault) lets the next good view attach.
//
// Every appearance of the name, every vanish and the end of the link start
// a new generation; a reply made for an older one (a proxy ready after the
// name went, an Attach answered after a failure) is not current.

class Link {
    constructor() {
        this._generation = 0;
        this._blocked = false;
        this._closed = false;
        this._attached = false;
        this._attaching = 0; // the token of the Attach waiting for its reply
        this._tokens = 0;
    }

    // appeared: the agent's name has an owner (again); the generation of
    // what is started for it.
    appeared() {
        this._next();
        this._blocked = false;
        return this._generation;
    }

    // vanished: the agent's name has no owner.
    vanished() {
        this._next();
        this._blocked = false;
    }

    // close: the extension is disabled; nothing is current any more.
    close() {
        this._next();
        this._closed = true;
    }

    get generation() {
        return this._generation;
    }

    current(generation) {
        return generation === this._generation;
    }

    // failed: something failed and the agent is told; badView: the failure
    // was the agent's answer, not a view.
    failed(badView) {
        if (!badView)
            this._blocked = true;
        this._detached();
    }

    // mayShow: a good view may be drawn, shown and attached to.
    mayShow() {
        return !this._blocked && !this._closed;
    }

    get attached() {
        return this._attached;
    }

    // startAttach: the token of an Attach to send now, or 0 when none is
    // due (attached, one waiting for its reply, or not to be shown).
    startAttach() {
        if (!this.mayShow() || this._attached || this._attaching !== 0)
            return 0;
        this._attaching = ++this._tokens;
        return this._attaching;
    }

    // attachDone: the Attach with token was answered (ok: without an
    // error). True when the extension now counts as attached (Ping starts).
    attachDone(token, ok) {
        if (token === 0 || token !== this._attaching)
            return false;
        this._attaching = 0;
        if (!ok || !this.mayShow())
            return false;
        this._attached = true;
        return true;
    }

    _next() {
        this._generation++;
        this._detached();
    }

    _detached() {
        this._attached = false;
        this._attaching = 0;
    }
}

function createLink() {
    return new Link();
}

// runEach runs each step in order; one that throws is passed to failed
// (when given) and the next step runs all the same. disable() ends with
// the button's step, so the button goes whatever threw before it (review
// A-m4): one left in the top bar after disable() stays there, frozen.
function runEach(steps, failed) {
    for (const step of steps) {
        try {
            step();
        } catch (e) {
            if (failed)
                failed(e);
        }
    }
}
