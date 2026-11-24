//go:build (linux || freebsd || openbsd || netbsd) && !android

// Added to the upstream v1.12.2 code; see PATCHED.md.

package systray

import (
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
