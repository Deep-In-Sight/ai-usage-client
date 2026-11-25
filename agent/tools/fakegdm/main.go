//go:build linux

// fakegdm stands in for GDM on a CI runner's system bus (agent/ci/
// check-panel-gnome.sh; not shipped): GNOME Shell creates its screen
// shield, and with it org.gnome.ScreenSaver and the lock, only when
// org.gnome.DisplayManager answers its Manager.Version (3.5.91 or later)
// on the system bus (misc/loginManager.js canLock). It owns that name and
// answers that property, nothing else, until SIGTERM or SIGINT. It prints
// "owns org.gnome.DisplayManager" once it does.
//
//	fakegdm [-address ADDR]   (default: the system bus)
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

const (
	name    = "org.gnome.DisplayManager"
	path    = dbus.ObjectPath("/org/gnome/DisplayManager/Manager")
	iface   = "org.gnome.DisplayManager.Manager"
	version = "46.0"
)

// serve exports the Version property on conn and takes the name.
func serve(conn *dbus.Conn) error {
	if _, err := prop.Export(conn, path, prop.Map{iface: {
		"Version": {Value: version, Writable: false, Emit: prop.EmitFalse},
	}}); err != nil {
		return err
	}
	reply, err := conn.RequestName(name, dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("%s is owned already", name)
	}
	return nil
}

func main() {
	addr := flag.String("address", "", "bus address (default: the system bus)")
	flag.Parse()
	fail := func(err error) {
		fmt.Fprintln(os.Stderr, "fakegdm:", err)
		os.Exit(1)
	}
	var conn *dbus.Conn
	var err error
	if *addr != "" {
		conn, err = dbus.Connect(*addr)
	} else {
		conn, err = dbus.ConnectSystemBus()
	}
	if err != nil {
		fail(err)
	}
	defer conn.Close()
	if err := serve(conn); err != nil {
		fail(err)
	}
	fmt.Println("owns " + name)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs
}
