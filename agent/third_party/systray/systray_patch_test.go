//go:build (linux || freebsd || openbsd || netbsd) && !android

// Added to the upstream v1.12.2 code; see PATCHED.md.

package systray

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"strings"
	"testing"
	"time"
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
