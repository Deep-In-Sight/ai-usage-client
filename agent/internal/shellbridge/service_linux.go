//go:build linux

package shellbridge

import (
	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

// ifaceXML is the interface (design 6.4), as the extension declares it.
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

// service is the exported object: its exported methods are the
// interface's methods. Each answers the current owner of org.gnome.Shell
// only (design 6.4: no other program of the session may fake a click or
// an attachment); old extensions' calls keep working (RF1: Attach takes
// any protocol, the view only gains fields).
type service struct{ b *bridge }

// export puts the object on conn: the interface, its Protocol property
// and the introspection data.
func export(conn *dbus.Conn, s *service) error {
	if err := conn.Export(s, dbus.ObjectPath(ObjectPath), Interface); err != nil {
		return err
	}
	if _, err := prop.Export(conn, dbus.ObjectPath(ObjectPath), prop.Map{Interface: {
		"Protocol": {Value: Protocol, Writable: false, Emit: prop.EmitFalse},
	}}); err != nil {
		return err
	}
	node := "<node>" + introspect.IntrospectDataString + prop.IntrospectDataString + ifaceXML + "</node>"
	return conn.Export(introspect.Introspectable(node), dbus.ObjectPath(ObjectPath), "org.freedesktop.DBus.Introspectable")
}

// shell refuses a caller that is not the current owner of org.gnome.Shell.
func (s *service) shell(sender dbus.Sender) *dbus.Error {
	if owner := s.b.nameOwner(shellName); owner == "" || owner != string(sender) {
		return dbus.NewError(errAccessDenied, []any{"only GNOME Shell may call the usage panel"})
	}
	return nil
}

// do runs f on the bridge's loop and waits for it; false when the bridge
// has ended.
func (s *service) do(f func()) bool {
	ran := make(chan struct{})
	select {
	case s.b.reqs <- func() { f(); close(ran) }:
	case <-s.b.done:
		return false
	}
	select {
	case <-ran:
		return true
	case <-s.b.done:
		return false
	}
}

func ended() *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.Failed", []any{"the bridge has ended"})
}

// Attach: the extension drew the panel; it is attached from now on.
func (s *service) Attach(sender dbus.Sender, extensionProtocol uint32, shellVersion string) (uint32, *dbus.Error) {
	if err := s.shell(sender); err != nil {
		return 0, err
	}
	if !s.do(func() { s.b.attach(string(sender), extensionProtocol, shellVersion) }) {
		return 0, ended()
	}
	return Protocol, nil
}

// Detach: the extension is disabled or failed to draw.
func (s *service) Detach(sender dbus.Sender) *dbus.Error {
	if err := s.shell(sender); err != nil {
		return err
	}
	if !s.do(func() {
		if s.b.caller == string(sender) {
			s.b.detach("detach")
		}
	}) {
		return ended()
	}
	return nil
}

// GetView is the panel as JSON (design 6.4).
func (s *service) GetView(sender dbus.Sender) (string, *dbus.Error) {
	if err := s.shell(sender); err != nil {
		return "", err
	}
	var v string
	if !s.do(func() { s.b.seen(string(sender)); v = s.b.view() }) {
		return "", ended()
	}
	return v, nil
}

// Click passes a click on an item of the current panel to the main
// process (plan PR-15); anything else is refused and goes nowhere.
func (s *service) Click(sender dbus.Sender, id string) *dbus.Error {
	if err := s.shell(sender); err != nil {
		return err
	}
	known := false
	if !s.do(func() {
		if known = clickable(s.b.panel, id); known {
			s.b.send(trayproto.Msg{Type: trayproto.TypeClick, ID: id})
		}
	}) {
		return ended()
	}
	if !known {
		return dbus.NewError(ErrUnknownItem, []any{"no such item: " + id})
	}
	return nil
}

// Ping: the extension is alive (every 30 s).
func (s *service) Ping(sender dbus.Sender) *dbus.Error {
	if err := s.shell(sender); err != nil {
		return err
	}
	if !s.do(func() { s.b.seen(string(sender)) }) {
		return ended()
	}
	return nil
}

// clickable: id is a row of p (the "More limits" row too), an action of p
// that is visible and enabled, or the new-version line while p has one
// (plan PR-15).
func clickable(p *trayproto.Panel, id string) bool {
	if p == nil || id == "" {
		return false
	}
	if id == "newversion" {
		return p.NewVersion != ""
	}
	for _, sec := range p.Sections {
		for _, r := range sec.Rows {
			if r.ID == id {
				return true
			}
		}
	}
	for _, a := range p.Actions {
		if a.ID == id {
			return a.Visible && a.Enabled
		}
	}
	return false
}
