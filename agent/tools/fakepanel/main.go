//go:build linux

// fakepanel is a stand-in for the AI Usage agent's session-bus bridge in
// CI and in previews: it owns local.AIUsage.Panel on
// $DBUS_SESSION_BUS_ADDRESS and answers the panel interface the GNOME
// extension calls as the bridge does (design 6.4; without the bridge's
// check that the caller is the shell). Not shipped.
//
//	fakepanel -view fixture:combined|fixture:states|FILE -mode MODE [-log FILE] [-next VIEW]
//
// The view is the usage panel of a picture fixture (internal/tray/
// panelfixture, serial 1) or a file's JSON as it is. MODE:
//
//	ok           GetView returns the view
//	badjson      GetView returns "{"
//	extrafields  unknown keys added to every object of the view
//	missing      fields of the view left out or null
//	hang         GetView never answers
//	vanish       the name is released 5 s after the first Attach and taken
//	             again 10 s later
//
// Each call is one log line (default: standard output): "Attach 1 46.0",
// "Detach", "GetView", "Click r.claude.0" (or "Click ID refused", which
// answers local.AIUsage.Panel1.Error.UnknownItem as the bridge would),
// "Ping"; also "owns local.AIUsage.Panel" once it does, "released …" and
// "owns … again" (vanish) and "ViewChanged N". SIGUSR1 raises the view's
// serial by one and emits ViewChanged with it; SIGUSR2 puts the -next view
// (as -view) in place with the next serial and emits ViewChanged.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray/panelfixture"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

const (
	busName        = "local.AIUsage.Panel"
	objectPath     = dbus.ObjectPath("/local/AIUsage/Panel")
	iface          = "local.AIUsage.Panel1"
	errUnknownItem = "local.AIUsage.Panel1.Error.UnknownItem"
	protocol       = uint32(1)
)

const ifaceXML = `<interface name="local.AIUsage.Panel1">
  <property name="Protocol" type="u" access="read"/>
  <method name="Attach">
    <arg type="u" direction="in" name="extension_protocol"/>
    <arg type="s" direction="in" name="shell_version"/>
    <arg type="u" direction="out" name="protocol"/>
  </method>
  <method name="Detach"/>
  <method name="GetView"><arg type="s" direction="out" name="view_json"/></method>
  <method name="Click"><arg type="s" direction="in" name="id"/></method>
  <method name="Ping"/>
  <signal name="ViewChanged"><arg type="t" name="serial"/></signal>
</interface>`

var modes = []string{"ok", "badjson", "extrafields", "missing", "hang", "vanish"}

// loadView is the view's JSON for -view.
func loadView(spec string) ([]byte, error) {
	var in tray.MenuInputs
	switch spec {
	case "fixture:combined":
		in = panelfixture.Combined()
	case "fixture:states":
		in = panelfixture.States()
	case "":
		return nil, errors.New("no -view")
	default:
		if strings.HasPrefix(spec, "fixture:") {
			return nil, fmt.Errorf("unknown fixture %q", spec)
		}
		return os.ReadFile(spec)
	}
	p := tray.BuildPanel(in)
	p.Serial = 1 // the agent's first panel
	return json.Marshal(p)
}

// withSerial is view with its serial set to n (view as it is when it is
// not a JSON object).
func withSerial(view []byte, n uint64) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(view, &m) != nil || m == nil {
		return view
	}
	m["serial"] = json.RawMessage(fmt.Sprint(n))
	b, err := json.Marshal(m)
	if err != nil {
		return view
	}
	return b
}

// addExtras adds keys no extension knows to every object in v.
func addExtras(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = addExtras(e)
		}
		x["zz_future"] = map[string]any{"list": []any{1, map[string]any{"deep": nil}}, "text": "x"}
		x["icon_v2"] = 7
	case []any:
		for i, e := range x {
			x[i] = addExtras(e)
		}
	}
	return v
}

// dropFields leaves out or nulls fields at every level of a view: the
// top's optional texts and lists, the sections' header texts, every row's
// numbers and texts but its id and label, and the rows of every section
// after the first.
func dropFields(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	for _, k := range []string{"new_version", "prompt", "top"} {
		delete(m, k)
	}
	m["actions"] = nil
	sections, _ := m["sections"].([]any)
	for i, s := range sections {
		sm, ok := s.(map[string]any)
		if !ok {
			continue
		}
		for _, k := range []string{"account", "updated", "problem", "stale"} {
			delete(sm, k)
		}
		if i > 0 {
			sm["rows"] = nil
			continue
		}
		rows, _ := sm["rows"].([]any)
		for _, r := range rows {
			if rm, ok := r.(map[string]any); ok {
				for _, k := range []string{"percent", "percent_text", "detail", "level", "more"} {
					delete(rm, k)
				}
			}
		}
	}
	return m
}

// transformed is view changed as mode says (ok, hang, vanish: as it is).
func transformed(view []byte, mode string) []byte {
	var f func(any) any
	switch mode {
	case "badjson":
		return []byte("{")
	case "extrafields":
		f = addExtras
	case "missing":
		f = dropFields
	default:
		return view
	}
	var v any
	if json.Unmarshal(view, &v) != nil {
		return view
	}
	b, err := json.Marshal(f(v))
	if err != nil {
		return view
	}
	return b
}

// clickable is the bridge's rule for a click (plan PR-15): a row of the
// view, an action that is visible and enabled, or the new-version line
// while there is one.
func clickable(p trayproto.Panel, id string) bool {
	if id == "" {
		return false
	}
	if id == "newversion" {
		return p.NewVersion != ""
	}
	for _, s := range p.Sections {
		for _, r := range s.Rows {
			if r.ID == id {
				return true
			}
		}
	}
	for _, a := range p.Actions {
		if a.ID == id && a.Visible && a.Enabled {
			return true
		}
	}
	return false
}

// panel answers the interface. Only the D-Bus methods are exported.
type panel struct {
	mode string
	conn *dbus.Conn

	mu       sync.Mutex
	out      io.Writer
	view     []byte // as loaded, serial set
	serial   uint64
	attaches int
	hung     chan struct{} // closed when fakepanel ends
}

func newPanel(conn *dbus.Conn, view []byte, mode string, out io.Writer) *panel {
	p := &panel{mode: mode, conn: conn, out: out, hung: make(chan struct{})}
	var head struct {
		Serial uint64 `json:"serial"`
	}
	_ = json.Unmarshal(view, &head)
	p.serial = head.Serial
	p.view = view
	return p
}

func (p *panel) logf(format string, a ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = fmt.Fprintf(p.out, format+"\n", a...)
}

// Attach answers the protocol; in mode vanish the first one starts the
// name's absence.
func (p *panel) Attach(_ dbus.Sender, extensionProtocol uint32, shellVersion string) (uint32, *dbus.Error) {
	p.logf("Attach %d %s", extensionProtocol, shellVersion)
	p.mu.Lock()
	p.attaches++
	first := p.attaches == 1
	p.mu.Unlock()
	if first && p.mode == "vanish" {
		go p.vanish(5*time.Second, 10*time.Second)
	}
	return protocol, nil
}

func (p *panel) Detach(dbus.Sender) *dbus.Error {
	p.logf("Detach")
	return nil
}

func (p *panel) GetView(dbus.Sender) (string, *dbus.Error) {
	p.logf("GetView")
	if p.mode == "hang" {
		<-p.hung
		return "", dbus.MakeFailedError(errors.New("fakepanel ended"))
	}
	p.mu.Lock()
	view := p.view
	p.mu.Unlock()
	return string(transformed(view, p.mode)), nil
}

func (p *panel) Click(_ dbus.Sender, id string) *dbus.Error {
	p.mu.Lock()
	var shown trayproto.Panel
	ok := json.Unmarshal(p.view, &shown) == nil && clickable(shown, id)
	p.mu.Unlock()
	if !ok {
		p.logf("Click %s refused", id)
		return &dbus.Error{Name: errUnknownItem, Body: []any{"no such item: " + id}}
	}
	p.logf("Click %s", id)
	return nil
}

func (p *panel) Ping(dbus.Sender) *dbus.Error {
	p.logf("Ping")
	return nil
}

// changed raises the serial and emits ViewChanged with it.
func (p *panel) changed() error {
	p.mu.Lock()
	p.serial++
	n := p.serial
	p.view = withSerial(p.view, n)
	p.mu.Unlock()
	if err := p.conn.Emit(objectPath, iface+".ViewChanged", n); err != nil {
		return err
	}
	p.logf("ViewChanged %d", n)
	return nil
}

// switchTo puts view in place with the next serial and emits ViewChanged
// with it (SIGUSR2, -next).
func (p *panel) switchTo(view []byte) error {
	p.mu.Lock()
	p.serial++
	n := p.serial
	p.view = withSerial(view, n)
	p.mu.Unlock()
	if err := p.conn.Emit(objectPath, iface+".ViewChanged", n); err != nil {
		return err
	}
	p.logf("ViewChanged %d", n)
	return nil
}

func (p *panel) vanish(after, away time.Duration) {
	time.Sleep(after)
	if _, err := p.conn.ReleaseName(busName); err != nil {
		p.logf("release failed: %v", err)
		return
	}
	p.logf("released %s", busName)
	time.Sleep(away)
	if err := own(p.conn); err != nil {
		p.logf("taking the name again failed: %v", err)
		return
	}
	p.logf("owns %s again", busName)
}

// export puts the object on conn: the interface, its Protocol property
// and the introspection data.
func export(conn *dbus.Conn, p *panel) error {
	if err := conn.Export(p, objectPath, iface); err != nil {
		return err
	}
	if _, err := prop.Export(conn, objectPath, prop.Map{iface: {
		"Protocol": {Value: protocol, Writable: false, Emit: prop.EmitFalse},
	}}); err != nil {
		return err
	}
	node := "<node>" + introspect.IntrospectDataString + prop.IntrospectDataString + ifaceXML + "</node>"
	return conn.Export(introspect.Introspectable(node), objectPath, "org.freedesktop.DBus.Introspectable")
}

func own(conn *dbus.Conn) error {
	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("%s is already owned", busName)
	}
	return nil
}

func main() {
	viewSpec := flag.String("view", "fixture:combined", "fixture:combined, fixture:states or a JSON file")
	mode := flag.String("mode", "ok", strings.Join(modes, "|"))
	logPath := flag.String("log", "", "call log (default: standard output)")
	nextSpec := flag.String("next", "", "the view SIGUSR2 puts in place (as -view)")
	flag.Parse()
	fail := func(err error) {
		fmt.Fprintln(os.Stderr, "fakepanel:", err)
		os.Exit(1)
	}
	known := false
	for _, m := range modes {
		known = known || m == *mode
	}
	if !known || flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	view, err := loadView(*viewSpec)
	if err != nil {
		fail(err)
	}
	var next []byte
	if *nextSpec != "" {
		if next, err = loadView(*nextSpec); err != nil {
			fail(err)
		}
	}
	out := io.Writer(os.Stdout)
	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			fail(err)
		}
		defer f.Close()
		out = f
	}
	addr := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if addr == "" {
		fail(errors.New("DBUS_SESSION_BUS_ADDRESS is not set"))
	}
	conn, err := dbus.Connect(addr)
	if err != nil {
		fail(err)
	}
	defer conn.Close()
	p := newPanel(conn, view, *mode, out)
	if err := export(conn, p); err != nil {
		fail(err)
	}
	if err := own(conn); err != nil {
		fail(err)
	}
	p.logf("owns %s", busName)
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGINT, syscall.SIGTERM)
loop:
	for s := range sigs {
		var err error
		switch s {
		case syscall.SIGUSR1:
			err = p.changed()
		case syscall.SIGUSR2:
			if next == nil {
				continue
			}
			err = p.switchTo(next)
		default:
			break loop
		}
		if err != nil {
			p.logf("emit failed: %v", err)
		}
	}
	close(p.hung)
}
