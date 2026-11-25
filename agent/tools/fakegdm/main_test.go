//go:build linux

package main

import (
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/fakeshell"
)

// GNOME Shell creates its screen shield (and so org.gnome.ScreenSaver)
// only when it finds a display manager: org.gnome.DisplayManager's
// Manager.Version, 3.5.91 or later, on the system bus. fakegdm answers it
// (here on a private bus).
func TestAnswersTheShellsQuestion(t *testing.T) {
	bus := fakeshell.NewBus(t, "")
	server, err := dbus.Connect(bus.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if err := serve(server); err != nil {
		t.Fatal(err)
	}
	client, err := dbus.Connect(bus.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	v, err := client.Object(name, path).GetProperty(iface + ".Version")
	if err != nil {
		t.Fatal(err)
	}
	if v.Value() != version {
		t.Fatalf("Version %v", v.Value())
	}
	if err := serve(server); err == nil {
		t.Fatal("the name was taken twice")
	}
}
