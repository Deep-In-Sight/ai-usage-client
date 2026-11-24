//go:build (linux || freebsd || openbsd || netbsd) && !android

// Not upstream code: this file was added to the copy of fyne.io/systray
// v1.12.2 in agent/third_party/systray of the AI Usage client (see
// PATCHED.md). It is under the AI Usage client's MIT licence (LICENSE at
// the repository's root), not the Apache License 2.0 of the copied code.

package systray

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"image"
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

// SetLabel before start-up is stored and lands in the exported properties.
func TestPropSpecHasTheLabel(t *testing.T) {
	SetLabel("71%", "100% ⚠")
	defer SetLabel("", "")
	instance.lock.Lock()
	spec := instance.createPropSpec()
	instance.lock.Unlock()
	item := spec["org.kde.StatusNotifierItem"]
	if v := item["XAyatanaLabel"].Value; v != "71%" {
		t.Fatalf("XAyatanaLabel %v", v)
	}
	if v := item["XAyatanaLabelGuide"].Value; v != "100% ⚠" {
		t.Fatalf("XAyatanaLabelGuide %v", v)
	}
	if item["XAyatanaLabel"].Writable || item["XAyatanaLabelGuide"].Writable {
		t.Fatal("label properties are writable")
	}
}

// nativeStart builds the spec, exports it and publishes props while it
// holds instance.lock (no SetLabel/SetIcon can fall between the spec and
// props), so createPropSpec must not take the lock itself.
func TestPropSpecRunsUnderTheCallersLock(t *testing.T) {
	done := make(chan struct{})
	go func() {
		instance.lock.Lock()
		defer instance.lock.Unlock()
		instance.createPropSpec()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("createPropSpec blocks on instance.lock")
	}
}

// Introspection lists the label properties and signal.
func TestIntrospectionHasTheLabel(t *testing.T) {
	i := itemIntrospection()
	if i.Name != "org.kde.StatusNotifierItem" {
		t.Fatalf("interface %s", i.Name)
	}
	props := map[string]string{}
	for _, p := range i.Properties {
		props[p.Name] = p.Type + " " + p.Access
	}
	for _, name := range []string{"XAyatanaLabel", "XAyatanaLabelGuide"} {
		if props[name] != "s read" {
			t.Fatalf("%s: %q", name, props[name])
		}
	}
	if props["IconPixmap"] == "" {
		t.Fatal("upstream properties lost")
	}
	found := false
	for _, s := range i.Signals {
		if s.Name == "XAyatanaNewLabel" {
			found = len(s.Args) == 2 && s.Args[0].Name == "label" && s.Args[0].Type == "s" &&
				s.Args[1].Name == "guide" && s.Args[1].Type == "s"
		}
	}
	if !found {
		t.Fatal("no XAyatanaNewLabel(s label, s guide)")
	}
	// the generated upstream data itself is unchanged
	for _, p := range i.Properties[:len(i.Properties)-2] {
		if p.Name == "XAyatanaLabel" {
			t.Fatal("upstream data modified")
		}
	}
}

// The M1 fix itself: nativeStart must lock, export the spec, store
// instance.props and unlock, in this order and with nothing in between, so
// no Set* call can fall between the spec and props. (The race needs a
// session bus and lasts microseconds; this structural check is
// deterministic and fails if the store moves back outside the lock.)
func TestNativeStartPublishesPropsUnderTheLock(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "systray_unix.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body []ast.Stmt
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "nativeStart" {
			body = fd.Body.List
		}
	}
	src := func(n ast.Node) string {
		var b strings.Builder
		if err := printer.Fprint(&b, fset, n); err != nil {
			t.Fatal(err)
		}
		return strings.Join(strings.Fields(b.String()), " ")
	}
	want := []string{
		"instance.lock.Lock()",
		"props, err := prop.Export(conn, path, instance.createPropSpec())",
		"if err == nil { instance.props = props }",
		"instance.lock.Unlock()",
	}
	for i := range body {
		if i+len(want) > len(body) {
			break
		}
		match := true
		for j, w := range want {
			if src(body[i+j]) != w {
				match = false
				break
			}
		}
		if match {
			return
		}
	}
	t.Fatal("nativeStart does not export and store the properties under one hold of instance.lock")
}

// sessionBus connects to the session bus, or skips the test when there is
// none (CI runs this module's tests under dbus-run-session). An address
// that is set but cannot be reached fails the test.
func sessionBus(t *testing.T) *dbus.Conn {
	t.Helper()
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		t.Skip("no session bus (run under dbus-run-session)")
	}
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// The session bus goes away while the tray runs (GNOME restarts it at every
// logout): setting the icon, label, title or tooltip afterwards logs the
// error instead of panicking (prop.SetMust panics when PropertiesChanged
// cannot be sent), and the values are still kept.
func TestSettersSurviveAClosedConnection(t *testing.T) {
	conn := sessionBus(t)
	instance.lock.Lock()
	props, err := prop.Export(conn, path, instance.createPropSpec())
	if err == nil {
		instance.conn, instance.props = conn, props
	}
	instance.lock.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		instance.lock.Lock()
		instance.conn, instance.props = nil, nil
		instance.lock.Unlock()
		SetLabel("", "")
		SetTitle("")
		SetTooltip("")
		SetIcon(nil)
	})
	conn.Close()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic after the connection closed: %v", r)
		}
	}()
	SetIcon(testPNG(t))
	SetLabel("57%", "100% ⚠")
	SetTitle("57%")
	SetTooltip("AI Usage")
	SetTemplateIcon(nil, testPNG(t))
	if v := props.GetMust("org.kde.StatusNotifierItem", "XAyatanaLabel"); v != "57%" {
		t.Fatalf("XAyatanaLabel %v", v)
	}
	if px := props.GetMust("org.kde.StatusNotifierItem", "IconPixmap").([]PX); len(px) != 1 || px[0].W != 2 {
		t.Fatalf("IconPixmap %v", px)
	}
	instance.lock.Lock()
	label, title := instance.label, instance.title
	instance.lock.Unlock()
	if label != "57%" || title != "57%" {
		t.Fatalf("stored label %q title %q", label, title)
	}
}

// ConnectionLost is closed when the tray's connection to the bus ends while
// it runs, so that the program can start the tray again on a new
// connection (the copy cannot reconnect by itself).
func TestConnectionLostWhenTheBusCloses(t *testing.T) {
	sessionBus(t).Close()
	select {
	case <-ConnectionLost():
		t.Fatal("lost before the tray started")
	default:
	}
	oldReady := systrayReady
	systrayReady = func() {}
	t.Cleanup(func() { systrayReady = oldReady })
	nativeStart()
	instance.lock.Lock()
	conn := instance.conn
	instance.lock.Unlock()
	if conn == nil {
		t.Fatal("nativeStart did not connect")
	}
	select {
	case <-ConnectionLost():
		t.Fatal("lost while connected")
	case <-time.After(200 * time.Millisecond):
	}
	conn.Close()
	select {
	case <-ConnectionLost():
	case <-time.After(5 * time.Second):
		t.Fatal("ConnectionLost not closed after the connection closed")
	}
	instance.lock.Lock()
	instance.conn, instance.props, instance.menuProps = nil, nil, nil
	instance.lock.Unlock()
}

// Quit after a start-up that never got a connection (no bus, or the bus
// went away during start-up) must not panic on the missing connection.
func TestNativeEndWithoutAConnection(t *testing.T) {
	instance.lock.Lock()
	old := instance.conn
	instance.conn = nil
	instance.lock.Unlock()
	t.Cleanup(func() {
		instance.lock.Lock()
		instance.conn = old
		instance.lock.Unlock()
	})
	oldExit, oldCalled := systrayExit, systrayExitCalled
	exited := false
	systrayExit, systrayExitCalled = func() { exited = true }, false
	t.Cleanup(func() { systrayExit, systrayExitCalled = oldExit, oldCalled })
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nativeEnd panicked: %v", r)
		}
	}()
	nativeEnd()
	if !exited {
		t.Fatal("onExit not called")
	}
}
