//go:build linux

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/fakeshell"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray/panelfixture"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func combinedJSON(t *testing.T) []byte {
	t.Helper()
	p := tray.BuildPanel(panelfixture.Combined())
	p.Serial = 1
	b, err := json.Marshal(p)
	must(t, err)
	return b
}

func TestLoadView(t *testing.T) {
	got, err := loadView("fixture:combined")
	must(t, err)
	if !bytes.Equal(got, combinedJSON(t)) {
		t.Fatalf("fixture:combined is\n%s", got)
	}
	states, err := loadView("fixture:states")
	must(t, err)
	var p trayproto.Panel
	must(t, json.Unmarshal(states, &p))
	if p.Serial != 1 || len(p.Sections) != 2 || !p.Sections[0].Stale || p.Sections[1].Problem == "" {
		t.Fatalf("fixture:states is %+v", p)
	}
	file := filepath.Join(t.TempDir(), "view.json")
	must(t, os.WriteFile(file, []byte(`{"serial":4,"odd":[1]}`), 0o644))
	got, err = loadView(file)
	must(t, err)
	if string(got) != `{"serial":4,"odd":[1]}` {
		t.Fatalf("a file's view is %s", got)
	}
	for _, bad := range []string{"", "fixture:other", filepath.Join(t.TempDir(), "absent.json")} {
		if _, err := loadView(bad); err == nil {
			t.Fatalf("-view %q accepted", bad)
		}
	}
}

func TestWithSerial(t *testing.T) {
	var v map[string]any
	must(t, json.Unmarshal(withSerial(combinedJSON(t), 7), &v))
	if v["serial"] != 7.0 || v["server"] != "Server connected · this PC fetches Claude, Codex" {
		t.Fatalf("%v", v)
	}
	if got := withSerial([]byte("{"), 7); string(got) != "{" {
		t.Fatalf("%s", got)
	}
}

// strip removes what addExtras adds.
func strip(v any) any {
	switch x := v.(type) {
	case map[string]any:
		delete(x, "zz_future")
		delete(x, "icon_v2")
		for k, e := range x {
			x[k] = strip(e)
		}
	case []any:
		for i, e := range x {
			x[i] = strip(e)
		}
	}
	return v
}

// objects calls f for every object in v but the added ones.
func objects(v any, f func(map[string]any)) {
	switch x := v.(type) {
	case map[string]any:
		f(x)
		for k, e := range x {
			if k != "zz_future" {
				objects(e, f)
			}
		}
	case []any:
		for _, e := range x {
			objects(e, f)
		}
	}
}

func TestModesChangeTheView(t *testing.T) {
	view := combinedJSON(t)
	for _, mode := range []string{"ok", "hang", "vanish"} {
		if !bytes.Equal(transformed(view, mode), view) {
			t.Fatalf("mode %s changes the view", mode)
		}
	}
	if string(transformed(view, "badjson")) != "{" {
		t.Fatal("badjson")
	}

	var orig, extra any
	must(t, json.Unmarshal(view, &orig))
	must(t, json.Unmarshal(transformed(view, "extrafields"), &extra))
	n := 0
	objects(extra, func(m map[string]any) {
		n++
		if _, ok := m["zz_future"]; !ok {
			t.Fatalf("an object without the extra key: %v", m)
		}
	})
	if n < 15 || !reflect.DeepEqual(strip(extra), orig) {
		t.Fatalf("extrafields: %d objects, or more than keys added", n)
	}

	var missing map[string]any
	must(t, json.Unmarshal(transformed(view, "missing"), &missing))
	for _, k := range []string{"new_version", "prompt", "top"} {
		if _, ok := missing[k]; ok {
			t.Fatalf("missing still has %s", k)
		}
	}
	sections := missing["sections"].([]any)
	first, second := sections[0].(map[string]any), sections[1].(map[string]any)
	if missing["actions"] != nil || second["rows"] != nil || first["account"] != nil || len(sections) != 2 {
		t.Fatalf("missing: %v", missing)
	}
	for _, r := range first["rows"].([]any) {
		if keys := len(r.(map[string]any)); keys != 2 {
			t.Fatalf("a row with %d keys: %v", keys, r)
		}
	}
}

// The bridge's click rule (plan PR-15).
func TestClickable(t *testing.T) {
	p := tray.BuildPanel(panelfixture.Combined())
	for id, want := range map[string]bool{
		"r.claude.0": true, "r.codex.1": true, "r.codex.9": false, "h.claude": false, "server": false,
		"refresh": true, "quit": true, "newversion": false, "": false,
	} {
		if got := clickable(p, id); got != want {
			t.Fatalf("clickable(%q) = %v", id, got)
		}
	}
	p.NewVersion = "New version available: 0.9.2"
	for i := range p.Actions {
		if p.Actions[i].ID == "refresh" {
			p.Actions[i].Enabled = false
		}
		if p.Actions[i].ID == "dashboard" {
			p.Actions[i].Visible = false
		}
	}
	for id, want := range map[string]bool{"newversion": true, "refresh": false, "dashboard": false, "quit": true} {
		if got := clickable(p, id); got != want {
			t.Fatalf("clickable(%q) = %v", id, got)
		}
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// privateBus starts a session bus of its own and returns its address.
// Needs dbus-daemon (skipped without it, a failure on CI).
func privateBus(t *testing.T) string {
	t.Helper()
	daemon := fakeshell.Daemon(t) // skipped without it; fails on CI
	cmd := exec.Command(daemon, "--session", "--nofork", "--print-address=1")
	stdout, err := cmd.StdoutPipe()
	must(t, err)
	must(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	addr, err := bufio.NewReader(stdout).ReadString('\n')
	must(t, err)
	return strings.TrimSpace(addr)
}

// The interface on a real bus, called as the extension calls it.
func TestOnAPrivateBus(t *testing.T) {
	addr := privateBus(t)
	server, err := dbus.Connect(addr)
	must(t, err)
	defer server.Close()
	var log syncBuffer
	p := newPanel(server, combinedJSON(t), "ok", &log)
	must(t, export(server, p))
	must(t, own(server))

	client, err := dbus.Connect(addr)
	must(t, err)
	defer client.Close()
	obj := client.Object(busName, objectPath)
	v, err := obj.GetProperty(iface + ".Protocol")
	must(t, err)
	if v.Value() != uint32(1) {
		t.Fatalf("Protocol %v", v.Value())
	}
	var proto uint32
	must(t, obj.Call(iface+".Attach", 0, uint32(1), "46.0").Store(&proto))
	if proto != 1 {
		t.Fatalf("Attach answered %d", proto)
	}
	var view string
	must(t, obj.Call(iface+".GetView", 0).Store(&view))
	if view != string(combinedJSON(t)) {
		t.Fatalf("GetView returned\n%s", view)
	}
	must(t, obj.Call(iface+".Click", 0, "r.claude.0").Err)
	var de dbus.Error
	if err := obj.Call(iface+".Click", 0, "r.codex.9").Err; !errors.As(err, &de) || de.Name != errUnknownItem {
		t.Fatalf("an unknown click answered %v", err)
	}
	must(t, obj.Call(iface+".Ping", 0).Err)
	must(t, obj.Call(iface+".Detach", 0).Err)
	var xml string
	must(t, client.Object(busName, objectPath).Call("org.freedesktop.DBus.Introspectable.Introspect", 0).Store(&xml))
	if !strings.Contains(xml, `<signal name="ViewChanged">`) || !strings.Contains(xml, "org.freedesktop.DBus.Properties") {
		t.Fatalf("introspection:\n%s", xml)
	}

	// a change: ViewChanged with the next serial, and the view carries it
	must(t, client.AddMatchSignal(dbus.WithMatchInterface(iface), dbus.WithMatchMember("ViewChanged")))
	sigs := make(chan *dbus.Signal, 4)
	client.Signal(sigs)
	must(t, p.changed())
	select {
	case s := <-sigs:
		if len(s.Body) != 1 || s.Body[0] != uint64(2) {
			t.Fatalf("signal %+v", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no ViewChanged")
	}
	must(t, obj.Call(iface+".GetView", 0).Store(&view))
	var got trayproto.Panel
	must(t, json.Unmarshal([]byte(view), &got))
	if got.Serial != 2 || len(got.Sections) != 2 {
		t.Fatalf("after the change: %+v", got)
	}

	want := "Attach 1 46.0\nGetView\nClick r.claude.0\nClick r.codex.9 refused\nPing\nDetach\nViewChanged 2\nGetView\n"
	if log.String() != want {
		t.Fatalf("log:\n%s\nwant:\n%s", log.String(), want)
	}
}
