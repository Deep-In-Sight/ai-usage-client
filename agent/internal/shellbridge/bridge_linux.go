//go:build linux

package shellbridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/gnomeext"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/text"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

// The bridge on the bus (design 6.4).
const (
	BusName            = "local.AIUsage.Panel"
	ObjectPath         = "/local/AIUsage/Panel"
	Interface          = "local.AIUsage.Panel1"
	Protocol    uint32 = 1                // the interface's protocol: fields and members are only added
	PingTimeout        = 90 * time.Second // attached only while a Ping or GetView came this recently
	ShellWait          = 30 * time.Second // the longest wait for GNOME Shell on the bus (plan PR-10)
	// ErrUnknownItem answers a click on an item the panel does not have
	// (plan PR-15).
	ErrUnknownItem = "local.AIUsage.Panel1.Error.UnknownItem"
)

// GNOME Shell's screen saver: the org.gnome.ScreenSaver interface of the
// shell's own name org.gnome.Shell.ScreenShield. The well-known name
// org.gnome.ScreenSaver is a separate GNOME service started on demand
// that passes calls and signals on; a call without auto-start does not
// reach it unless something else started it (measured on CI, plan Task
// 15).
const (
	screenShieldName = "org.gnome.Shell.ScreenShield"
	screenSaverIface = "org.gnome.ScreenSaver"
	screenSaverPath  = "/org/gnome/ScreenSaver"
)

const (
	shellName       = "org.gnome.Shell"
	busName         = "org.freedesktop.DBus" // the bus itself: the sender of its signals
	errAccessDenied = "org.freedesktop.DBus.Error.AccessDenied"
	callTimeout     = 5 * time.Second  // the bridge's own calls on the bus
	versionRetry    = time.Second      // a shell without its ShellVersion yet is asked again this often
	lockRecheck     = 30 * time.Second // a detach held for a lock asks the screen saver again this often (review m3)
	// A lock disables the extensions (their Detach) when it starts; the
	// screen saver says it is active only once the lock screen is shown
	// (GNOME's screenShield.js), so a Detach whose first answer is "not
	// active" waits this long for the lock before it is told.
	lockSettle = 2 * time.Second
	// lockTries: GetActive failing or unanswered this many times in a row
	// tells a held detach (about lockRecheck × 2 + callTimeout after the
	// first: review H2)
	lockTries = 3
	// dialRetry: a missing or dead session bus is dialled again this often
	// (as the icon helpers do: trayhelper's hostRetry)
	dialRetry = 2 * time.Second
	// a newer agent's bridge took the name (the newest agent wins)
	exitReplaced = trayproto.ExitRefused
)

// lockAskTimeout is how long GetActive may take (a variable for tests).
var lockAskTimeout = callTimeout

// Run serves the panel until quit or EOF (0), the loss of the session bus
// it was on (75) or an unsupported shell (78); a bus that is not there yet
// is waited for (dial). in is the main process's lines after the hello;
// out takes the bridge's lines.
func Run(ctx context.Context, in *bufio.Reader, out io.Writer, o Options) int {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Clock == nil {
		o.Clock = clock.Real{}
	}
	b := &bridge{o: o, out: out, reqs: make(chan func()), done: make(chan struct{}), locks: make(chan lockAnswer, 4),
		askTimeout: lockAskTimeout}
	defer close(b.done)
	lines := make(chan trayproto.Msg, 16)
	go func() {
		defer close(lines)
		for {
			m, err := trayproto.Decode(in)
			if err != nil {
				return
			}
			select {
			case lines <- m:
			case <-b.done:
				return
			}
		}
	}()
	conn, code := b.dial(ctx, lines)
	if conn == nil {
		return code
	}
	defer conn.Close()
	b.conn = conn
	return b.run(ctx, lines)
}

// dial connects to the session bus. A missing or dead bus (a logout the
// agent outlived, a start before the session's bus is up) is dialled again
// every dialRetry in this process, as the icon helpers wait for theirs:
// ending with 75 again and again would count as failures and stop the
// bridge for minutes, so the next login's panel would wait for it (lost-bus
// ruling, 4b re-review). Views and notices that come meanwhile are kept.
// nil and the exit code when quit, the end of the input or ctx came first.
func (b *bridge) dial(ctx context.Context, lines <-chan trayproto.Msg) (*dbus.Conn, int) {
	conn, err := gnomeext.DialSession(b.o.Getenv)
	if err == nil {
		return conn, 0
	}
	log.Printf("%v: waiting for the session bus", err)
	retry := b.o.Clock.After(dialRetry)
	for {
		select {
		case <-ctx.Done():
			return nil, trayproto.ExitOK
		case m, ok := <-lines:
			if !ok || m.Type == trayproto.TypeQuit {
				return nil, trayproto.ExitOK
			}
			switch {
			case m.Type == trayproto.TypeView && m.Panel != nil:
				b.panel, b.serial = m.Panel, m.Panel.Serial // nobody to signal yet
			case m.Type == trayproto.TypeNotice:
				b.notices = append(b.notices, m.Text)
			}
		case <-retry:
			if conn, err := gnomeext.DialSession(b.o.Getenv); err == nil {
				log.Print("the session bus is there")
				return conn, 0
			}
			retry = b.o.Clock.After(dialRetry)
		}
	}
}

// bridge is one run. The D-Bus methods hand their work to the loop (reqs);
// everything below conn is the loop's alone.
type bridge struct {
	o     Options
	conn  *dbus.Conn
	out   io.Writer
	reqs  chan func()
	done  chan struct{}   // closed when Run ends
	locks chan lockAnswer // GetActive's answers while a detach is held
	// how long GetActive may take (read once: tests change lockAskTimeout)
	askTimeout time.Duration

	panel     *trayproto.Panel // the newest view (nil: none yet)
	serial    uint64           // its serial, as signalled
	notices   []string         // notices that came before the bus did
	supported bool             // the main process heard that a supported shell answers
	owner     string           // the unique name that owns org.gnome.Shell ("": none)
	saver     string           // the unique name that owns org.gnome.Shell.ScreenShield ("": none)
	caller    string           // the attached extension's unique name ("": none)
	lapsed    string           // the caller after a Ping lapse: its next Ping or GetView attaches it again (review I2)
	lastSeen  time.Time        // its last Ping or GetView
	ping      <-chan time.Time // when its PingTimeout may have passed
	told      bool             // what the main process heard last: attached
	held      string           // a detach not told yet, while the screen is locked: its why
	heldGen   int              // counts held detaches: an answer for an older one is void (review m2)
	recheck   <-chan time.Time // when the held detach asks the screen saver again (review m3)
	settle    <-chan time.Time // when a Detach answered "not active" at first is asked again (lockSettle)
	settled   bool             // the held detach had its first answer
	lockFails int              // GetActive failed this many times in a row for the held detach (review H2)
	lockLog   int              // the held detach whose lock was logged
}

// lockAnswer is GetActive's answer for the held detach gen.
type lockAnswer struct {
	gen     int
	locked  bool // the screen saver is active
	failed  bool // GetActive was not answered in time, or answered an error
	noOwner bool // nobody owns the screen saver's name: no lock (the shell left)
}

type version struct {
	major int
	text  string
	err   error
}

func (b *bridge) run(ctx context.Context, lines <-chan trayproto.Msg) int {
	sigs := make(chan *dbus.Signal, 64)
	b.conn.Signal(sigs)
	for _, name := range []string{shellName, screenShieldName} {
		if err := b.conn.AddMatchSignal(dbus.WithMatchSender(busName), dbus.WithMatchInterface(busName),
			dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, name)); err != nil {
			log.Printf("session bus: %v", err)
			return trayproto.ExitSessionLost
		}
	}
	if err := b.conn.AddMatchSignal(dbus.WithMatchSender(screenShieldName), dbus.WithMatchObjectPath(screenSaverPath),
		dbus.WithMatchInterface(screenSaverIface), dbus.WithMatchMember("ActiveChanged")); err != nil {
		log.Printf("session bus: %v", err)
		return trayproto.ExitSessionLost
	}
	if err := export(b.conn, &service{b}); err != nil {
		log.Printf("session bus: %v", err)
		return trayproto.ExitSessionLost
	}
	reply, err := b.conn.RequestName(BusName, dbus.NameFlagAllowReplacement|dbus.NameFlagReplaceExisting|dbus.NameFlagDoNotQueue)
	if err != nil {
		log.Printf("session bus: %v", err)
		return trayproto.ExitSessionLost
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		log.Printf("%s is owned by another program", BusName)
		return exitReplaced
	}
	defer func() { _, _ = b.conn.ReleaseName(BusName) }()
	b.owner, b.saver = b.nameOwner(shellName), b.nameOwner(screenShieldName)
	for _, n := range b.notices {
		go b.notify(n)
	}
	b.notices = nil

	// until GNOME Shell's version is known: waiting
	waiting, asking := true, false
	deadline := b.o.Clock.After(ShellWait)
	var retry <-chan time.Time
	versions := make(chan version, 1)
	ask := func() {
		if !waiting || asking || retry != nil || b.owner == "" {
			return
		}
		asking = true
		go func() {
			c, cancel := context.WithTimeout(context.Background(), callTimeout)
			defer cancel()
			major, v, err := gnomeext.ShellMajor(c, b.conn)
			versions <- version{major, v, err}
		}()
	}
	ask()
	enabled := make(chan bool, 1)
	for {
		select {
		case <-ctx.Done():
			return trayproto.ExitOK
		case <-b.conn.Context().Done():
			log.Print("the connection to the session bus was lost")
			return trayproto.ExitSessionLost
		case m, ok := <-lines:
			if !ok || m.Type == trayproto.TypeQuit {
				return trayproto.ExitOK
			}
			b.line(m)
		case f := <-b.reqs:
			f()
		case s, ok := <-sigs:
			if !ok {
				log.Print("the connection to the session bus was lost")
				return trayproto.ExitSessionLost
			}
			if code, end := b.signal(s); end {
				return code
			}
			ask()
		case <-deadline:
			if waiting {
				why := "no shell"
				if b.owner != "" {
					why = "no shell version"
				}
				return b.unsupported(why)
			}
		case <-retry:
			retry = nil
			ask()
		case v := <-versions:
			asking = false
			switch {
			case !waiting:
			case v.err != nil:
				retry = b.o.Clock.After(versionRetry)
			default:
				if _, ok := gnomeext.FormFor(v.major); !ok {
					return b.unsupported("unsupported " + v.text)
				}
				waiting, deadline = false, nil
				b.ready(v.major, v.text, enabled)
			}
		case ok := <-enabled:
			if ok {
				b.send(trayproto.Msg{Type: trayproto.TypeShell, Enabled: true})
			}
		case a := <-b.locks:
			b.lockAnswer(a)
		case <-b.settle:
			b.settle = nil
			if b.held != "" {
				b.askLocked()
			}
		case <-b.recheck:
			b.recheck = nil
			if b.held != "" {
				b.askLocked()
			}
		case <-b.ping:
			b.pingDue()
		}
	}
}

// unsupported says the session's shell is not one the panel supports and
// gives the exit code.
func (b *bridge) unsupported(why string) int {
	log.Printf("no usage panel on this desktop (%s)", why)
	f := false
	b.send(trayproto.Msg{Type: trayproto.TypeShell, Supported: &f, Why: why})
	return trayproto.ExitUnsupported
}

// ready: a supported shell. The extension's files are made current for it
// (design 6.6: at every start), the main process hears that the shell is
// supported (it may then judge an attach wait without an attach: review
// I1), and the extension is turned on once when the hello asked for it.
func (b *bridge) ready(major int, v string, enabled chan<- bool) {
	log.Printf("GNOME Shell %s", v)
	if dir, err := gnomeext.Dir(b.o.Getenv, b.o.Home); err != nil {
		log.Printf("the panel extension's folder: %v", err)
	} else if wrote, err := gnomeext.Write(dir, major); err != nil {
		log.Printf("could not write the panel extension: %v", err)
	} else if wrote {
		log.Printf("wrote the panel extension (GNOME %d)", major)
	}
	t := true
	b.send(trayproto.Msg{Type: trayproto.TypeShell, Supported: &t})
	b.supported = true
	if !b.o.EnableOnce {
		return
	}
	go func() {
		c, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		ok, err := gnomeext.Enable(c, b.conn, gnomeext.UUID)
		switch {
		case err != nil:
			log.Printf("turning the panel extension on: %v", err)
		case !ok:
			log.Print("the shell has not read the panel extension yet (it does at the next login)")
		default:
			log.Print("turned the panel extension on")
		}
		enabled <- ok && err == nil
	}()
}

// line handles a line of the main process.
func (b *bridge) line(m trayproto.Msg) {
	switch m.Type {
	case trayproto.TypeView:
		if m.Panel == nil {
			return
		}
		b.panel = m.Panel
		if m.Panel.Serial != b.serial {
			b.serial = m.Panel.Serial
			if err := b.conn.Emit(dbus.ObjectPath(ObjectPath), Interface+".ViewChanged", b.serial); err != nil {
				log.Printf("ViewChanged: %v", err)
			}
		}
	case trayproto.TypeNotice:
		go b.notify(m.Text)
	}
}

// signal handles a signal; end: the bridge ends with code. The bus's
// signals count only when the bus sent them, ActiveChanged only when the
// screen saver did: another program may send any signal to the bridge
// (review m1).
func (b *bridge) signal(s *dbus.Signal) (code int, end bool) {
	switch s.Name {
	case busName + ".NameLost":
		if s.Sender == busName && len(s.Body) == 1 && s.Body[0] == BusName {
			log.Printf("another agent took %s", BusName)
			return exitReplaced, true
		}
	case busName + ".NameOwnerChanged":
		if s.Sender != busName || len(s.Body) != 3 {
			break
		}
		owner, _ := s.Body[2].(string)
		switch s.Body[0] {
		case shellName:
			prev := b.owner
			b.owner = owner
			if b.lapsed != "" && b.owner != b.lapsed {
				b.lapsed = "" // the lapsed extension left the bus: forgotten (review I2)
			}
			left := prev != "" && owner != prev
			switch {
			case b.caller != "" && b.owner != b.caller:
				b.detach("owner") // the shell went away or was replaced
			case left && b.held != "":
				// a detach held for a lock is told, when it is, as the shell's
				// leaving (review H1)
				b.held = "owner"
			case left && b.supported && !b.told:
				// no extension attached (it detached before, or never did): the
				// main process still learns that no supported shell answers now,
				// so a logout does not remove the gnome-panel-attached marker
				// (review H1; design 6.4)
				log.Print("GNOME Shell left the session bus")
				f := false
				b.send(trayproto.Msg{Type: trayproto.TypeShell, Attached: &f, Why: "owner"})
			}
		case screenShieldName:
			b.saver = owner
		}
	case screenSaverIface + ".ActiveChanged":
		if s.Sender != b.saver || b.saver == "" || s.Path != screenSaverPath || len(s.Body) != 1 || b.held == "" {
			break
		}
		switch s.Body[0] {
		case false:
			b.tell(false, b.held)
		case true:
			if b.settle != nil { // the lock a Detach came just before
				b.settle = nil
				b.lockHeld()
			}
		}
	}
	return 0, false
}

// attach: the extension drew the panel (design 6.4).
func (b *bridge) attach(caller string, protocol uint32, shellVersion string) {
	if b.caller == "" {
		log.Printf("the panel extension attached (protocol %d, GNOME Shell %s)", protocol, shellVersion)
	}
	b.caller, b.lapsed, b.held, b.recheck, b.settle = caller, "", "", nil, nil
	b.lastSeen = b.o.Clock.Now()
	b.ping = b.o.Clock.After(PingTimeout)
	if !b.told {
		b.tell(true, "")
	}
}

// seen: a Ping or GetView of the attached extension; of the extension
// whose Pings lapsed, it is attached again (it does not know it was
// dropped and never calls Attach again: review I2).
func (b *bridge) seen(sender string) {
	switch {
	case sender == "":
	case sender == b.caller:
		b.lastSeen = b.o.Clock.Now()
	case sender == b.lapsed && b.caller == "":
		log.Print("the panel extension answers again")
		b.caller, b.lapsed, b.held, b.recheck, b.settle = sender, "", "", nil, nil
		b.lastSeen = b.o.Clock.Now()
		b.ping = b.o.Clock.After(PingTimeout)
		if !b.told {
			b.tell(true, "")
		}
	}
}

// pingDue ends the attachment when the extension said nothing for
// PingTimeout (it stopped working: the icons come at once, plan PR-11).
func (b *bridge) pingDue() {
	b.ping = nil
	if b.caller == "" {
		return
	}
	if quiet := b.o.Clock.Now().Sub(b.lastSeen); quiet < PingTimeout {
		b.ping = b.o.Clock.After(PingTimeout - quiet)
		return
	}
	log.Printf("no Ping from the panel extension for %s", PingTimeout)
	b.caller, b.lapsed, b.held = "", b.caller, ""
	if b.told {
		b.tell(false, "ping")
	}
}

// detach ends the attachment and its Ping timer. The main process hears
// it at once, unless the screen is locked (GNOME disables extensions while
// it is): then only once it is unlocked and the extension has not
// attached again by then (plan PR-11, ruling F14).
func (b *bridge) detach(why string) {
	log.Printf("the panel extension went away (%s)", why)
	b.caller, b.ping = "", nil
	if !b.told {
		return
	}
	b.held, b.recheck, b.settle, b.settled, b.lockFails = why, nil, nil, false, 0
	b.heldGen++
	b.askLocked()
}

// lockAnswer handles GetActive's answer for a held detach. The detach is
// told only when the screen saver says it is not active, or nobody owns
// it (the shell left); a failed or unanswered ask is not "unlocked" (a
// busy shell during a real lock must not bring the icons up): it is asked
// again at the next recheck, and lockTries failures in a row tell it
// (review H2). A Detach first answered "not active" waits lockSettle for
// a lock that is starting.
func (b *bridge) lockAnswer(a lockAnswer) {
	if b.held == "" || a.gen != b.heldGen {
		return // the extension attached again meanwhile, or a newer detach is held
	}
	if a.failed && b.saver == "" {
		a.failed, a.noOwner = false, true // its owner left (NameOwnerChanged came first)
	}
	first := !b.settled
	b.settled = true
	switch {
	case a.failed:
		b.lockFails++
		if b.lockFails >= lockTries {
			log.Printf("the screen saver did not answer %d times in a row: the panel's absence is told", lockTries)
			b.tell(false, b.held)
			return
		}
		b.recheck = b.o.Clock.After(lockRecheck)
	case a.locked:
		b.lockFails = 0
		b.lockHeld()
	case first && !a.noOwner && b.held == "detach":
		b.lockFails = 0
		b.settle = b.o.Clock.After(lockSettle) // the lock may be starting
	default:
		b.tell(false, b.held)
	}
}

// lockHeld: the screen is locked; the held detach waits for the unlock,
// asking again every lockRecheck.
func (b *bridge) lockHeld() {
	if b.lockLog != b.heldGen {
		b.lockLog = b.heldGen
		log.Print("the screen is locked: the panel's absence is told once it is unlocked")
	}
	b.recheck = b.o.Clock.After(lockRecheck)
}

// askLocked asks the screen saver whether the screen is locked, for the
// held detach; while it is, it is asked again every lockRecheck: without
// its ActiveChanged at the unlock, panel and icon are both missing for at
// most that long (review m3).
func (b *bridge) askLocked() {
	gen := b.heldGen
	go func() {
		c, cancel := context.WithTimeout(context.Background(), b.askTimeout)
		defer cancel()
		var locked bool
		err := b.conn.Object(screenShieldName, screenSaverPath).CallWithContext(c, screenSaverIface+".GetActive",
			dbus.FlagNoAutoStart).Store(&locked)
		a := lockAnswer{gen: gen, locked: err == nil && locked}
		switch name := dbusErrorName(err); {
		case err == nil:
		case name == "org.freedesktop.DBus.Error.ServiceUnknown" || name == "org.freedesktop.DBus.Error.NameHasNoOwner":
			a.noOwner = true
		default:
			a.failed = true
		}
		select {
		case b.locks <- a:
		case <-b.done:
		}
	}()
}

// dbusErrorName is the D-Bus error name of err ("": none, or not a D-Bus
// error such as a timeout).
func dbusErrorName(err error) string {
	var de dbus.Error
	if errors.As(err, &de) {
		return de.Name
	}
	var pde *dbus.Error
	if errors.As(err, &pde) && pde != nil {
		return pde.Name
	}
	return ""
}

// tell sends the main process whether the extension is attached.
func (b *bridge) tell(attached bool, why string) {
	b.told, b.held, b.recheck, b.settle = attached, "", nil, nil
	b.send(trayproto.Msg{Type: trayproto.TypeShell, Attached: &attached, Why: why})
}

func (b *bridge) send(m trayproto.Msg) {
	m.V = trayproto.Version
	_ = trayproto.Encode(b.out, m) // a closed output: the main process is gone, standard input ends too
}

// view is GetView's answer: the newest panel, or an empty one before the
// first view (the extension shows nothing and waits for ViewChanged).
func (b *bridge) view() string {
	p := b.panel
	if p == nil {
		p = &trayproto.Panel{Sections: []trayproto.PanelSection{}, Actions: []trayproto.PanelAction{}, Top: []trayproto.PanelTop{}}
	}
	j, err := json.Marshal(*p)
	if err != nil {
		return "{}"
	}
	return string(j)
}

// nameOwner is the unique name that owns name ("": none).
func (b *bridge) nameOwner(name string) string {
	c, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	var owner string
	if err := b.conn.BusObject().CallWithContext(c, "org.freedesktop.DBus.GetNameOwner", 0, name).Store(&owner); err != nil {
		return ""
	}
	return owner
}

// notify shows the running notice through the desktop's notifications
// (design 6.4: while the panel is attached there is no icon to show it).
func (b *bridge) notify(body string) {
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := b.conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications").CallWithContext(c,
		"org.freedesktop.Notifications.Notify", 0, text.App, uint32(0), "", text.App, body, []string{},
		map[string]dbus.Variant{}, int32(10000)).Err
	if err != nil {
		log.Printf("notice: %v", err)
	}
}
