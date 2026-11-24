package main

import (
	"bytes"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestRegisterIsLogged(t *testing.T) {
	var b bytes.Buffer
	w := &watcher{out: &b}
	if err := w.RegisterStatusNotifierItem(dbus.Sender(":1.42"), "/StatusNotifierItem"); err != nil {
		t.Fatal(err)
	}
	if got := b.String(); got != "registered :1.42 /StatusNotifierItem\n" {
		t.Fatalf("%q", got)
	}
}

func TestLabelSignalLine(t *testing.T) {
	for _, c := range []struct {
		sig  *dbus.Signal
		want string
	}{
		{&dbus.Signal{Sender: ":1.42", Name: "org.kde.StatusNotifierItem.XAyatanaNewLabel", Body: []any{"57%", "100% ⚠"}},
			"label :1.42 \"57%\" \"100% ⚠\"\n"},
		{&dbus.Signal{Sender: ":1.42", Name: "org.kde.StatusNotifierItem.NewTitle", Body: []any{}}, ""},
		{&dbus.Signal{Sender: ":1.42", Name: "org.kde.StatusNotifierItem.XAyatanaNewLabel", Body: []any{"57%"}}, "label :1.42 malformed [57%]\n"},
	} {
		if got := signalLine(c.sig); got != c.want {
			t.Fatalf("%v: got %q, want %q", c.sig.Name, got, c.want)
		}
	}
}
