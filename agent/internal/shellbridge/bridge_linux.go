//go:build linux

package shellbridge

import (
	"bufio"
	"context"
	"encoding/json"
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

const (
	shellName       = "org.gnome.Shell"
	screenSaverName = "org.gnome.ScreenSaver"
	screenSaverPath = "/org/gnome/ScreenSaver"
	errAccessDenied = "org.freedesktop.DBus.Error.AccessDenied"
	callTimeout     = 5 * time.Second // the bridge's own calls on the bus
	versionRetry    = time.Second     // a shell without its ShellVersion yet is asked again this often
	// a newer agent's bridge took the name (the newest agent wins)
	exitReplaced = trayproto.ExitRefused
)

// Run serves the panel until quit or EOF (0), the loss of the session bus
// (75) or an unsupported shell (78). in is the main process's lines after
// the hello; out takes the bridge's lines.
func Run(ctx context.Context, in *bufio.Reader, out io.Writer, o Options) int {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Clock == nil {
		o.Clock = clock.Real{}
	}
	b := &bridge{o: o, out: out, reqs: make(chan func()), done: make(chan struct{}), locks: make(chan bool, 4)}
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
	conn, err := gnomeext.DialSession(o.Getenv)
	if err != nil {
		log.Print(err)
		return trayproto.ExitSessionLost
	}
	defer conn.Close()
	b.conn = conn
	return b.run(ctx, lines)
}

// bridge is one run. The D-Bus methods hand their work to the loop (reqs);
// everything below conn is the loop's alone.
type bridge struct {
	o     Options
	conn  *dbus.Conn
	out   io.Writer
	reqs  chan func()
	done  chan struct{} // closed when Run ends
	locks chan bool     // GetActive's answers after a detach

	panel    *trayproto.Panel // the newest view (nil: none yet)
	serial   uint64           // its serial, as signalled
	owner    string           // the unique name that owns org.gnome.Shell ("": none)
	caller   string           // the attached extension's unique name ("": none)
	lastSeen time.Time        // its last Ping or GetView
	ping     <-chan time.Time // when its PingTimeout may have passed
	told     bool             // what the main process heard last: attached
	held     string           // a detach not told yet, while the screen is locked: its why
}

type version struct {
	major int
	text  string
	err   error
}

func (b *bridge) run(ctx context.Context, lines <-chan trayproto.Msg) int {
	sigs := make(chan *dbus.Signal, 64)
	b.conn.Signal(sigs)
	if err := b.conn.AddMatchSignal(dbus.WithMatchSender("org.freedesktop.DBus"), dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, shellName)); err != nil {
		log.Printf("session bus: %v", err)
		return trayproto.ExitSessionLost
	}
	if err := b.conn.AddMatchSignal(dbus.WithMatchObjectPath(screenSaverPath), dbus.WithMatchInterface(screenSaverName),
		dbus.WithMatchMember("ActiveChanged")); err != nil {
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
	b.owner = b.nameOwner(shellName)

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
		case locked := <-b.locks:
			if b.held == "" {
				break
			}
			if locked {
				log.Print("the screen is locked: the panel's absence is told once it is unlocked")
				break
			}
			b.tell(false, b.held)
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
// (design 6.6: at every start), and the extension is turned on once when
// the hello asked for it.
func (b *bridge) ready(major int, v string, enabled chan<- bool) {
	log.Printf("GNOME Shell %s", v)
	if dir, err := gnomeext.Dir(b.o.Getenv, b.o.Home); err != nil {
		log.Printf("the panel extension's folder: %v", err)
	} else if wrote, err := gnomeext.Write(dir, major); err != nil {
		log.Printf("could not write the panel extension: %v", err)
	} else if wrote {
		log.Printf("wrote the panel extension (GNOME %d)", major)
	}
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

// signal handles a signal; end: the bridge ends with code.
func (b *bridge) signal(s *dbus.Signal) (code int, end bool) {
	switch s.Name {
	case "org.freedesktop.DBus.NameLost":
		if len(s.Body) == 1 && s.Body[0] == BusName {
			log.Printf("another agent took %s", BusName)
			return exitReplaced, true
		}
	case "org.freedesktop.DBus.NameOwnerChanged":
		if len(s.Body) == 3 && s.Body[0] == shellName {
			b.owner, _ = s.Body[2].(string)
			if b.caller != "" && b.owner != b.caller {
				b.detach("owner") // the shell went away or was replaced
			}
		}
	case screenSaverName + ".ActiveChanged":
		if len(s.Body) == 1 && s.Body[0] == false && b.held != "" {
			b.tell(false, b.held)
		}
	}
	return 0, false
}

// attach: the extension drew the panel (design 6.4).
func (b *bridge) attach(caller string, protocol uint32, shellVersion string) {
	if b.caller == "" {
		log.Printf("the panel extension attached (protocol %d, GNOME Shell %s)", protocol, shellVersion)
	}
	b.caller, b.held = caller, ""
	b.lastSeen = b.o.Clock.Now()
	b.ping = b.o.Clock.After(PingTimeout)
	if !b.told {
		b.tell(true, "")
	}
}

// seen: a Ping or GetView of the attached extension.
func (b *bridge) seen(sender string) {
	if sender != "" && sender == b.caller {
		b.lastSeen = b.o.Clock.Now()
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
	b.caller, b.held = "", ""
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
	b.held = why
	go func() {
		c, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		var locked bool
		err := b.conn.Object(screenSaverName, screenSaverPath).CallWithContext(c, screenSaverName+".GetActive",
			dbus.FlagNoAutoStart).Store(&locked)
		select {
		case b.locks <- err == nil && locked:
		case <-b.done:
		}
	}()
}

// tell sends the main process whether the extension is attached.
func (b *bridge) tell(attached bool, why string) {
	b.told, b.held = attached, ""
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
