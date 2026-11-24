// fakewatcher is a stand-in for a Linux desktop's tray host in CI: it owns
// org.kde.StatusNotifierWatcher on the session bus, so the agent shows its
// tray there, and prints what the tray does.
//
//	fakewatcher watch            print "registered <sender> <arg>" for each
//	                             item that registers and
//	                             "label <sender> <label> <guide>" (quoted) for
//	                             each XAyatanaNewLabel signal
//	fakewatcher get SERVICE PROP print a property of SERVICE's
//	                             /StatusNotifierItem (org.kde.StatusNotifierItem)
package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"

	"github.com/godbus/dbus/v5"
)

const (
	watcherName = "org.kde.StatusNotifierWatcher"
	watcherPath = "/StatusNotifierWatcher"
	itemIface   = "org.kde.StatusNotifierItem"
	itemPath    = "/StatusNotifierItem"
	labelSignal = itemIface + ".XAyatanaNewLabel"
)

type watcher struct {
	mu  sync.Mutex
	out io.Writer
}

func (w *watcher) print(s string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, _ = io.WriteString(w.out, s)
}

// RegisterStatusNotifierItem is the watcher method an item calls.
func (w *watcher) RegisterStatusNotifierItem(sender dbus.Sender, service string) *dbus.Error {
	w.print(fmt.Sprintf("registered %s %s\n", sender, service))
	return nil
}

// RegisterStatusNotifierHost is accepted and ignored.
func (w *watcher) RegisterStatusNotifierHost(string) *dbus.Error { return nil }

// signalLine is the line printed for a signal ("" for other signals).
func signalLine(sig *dbus.Signal) string {
	if sig.Name != labelSignal {
		return ""
	}
	if len(sig.Body) == 2 {
		label, ok1 := sig.Body[0].(string)
		guide, ok2 := sig.Body[1].(string)
		if ok1 && ok2 {
			return fmt.Sprintf("label %s %s %s\n", sig.Sender, strconv.Quote(label), strconv.Quote(guide))
		}
	}
	return fmt.Sprintf("label %s malformed %v\n", sig.Sender, sig.Body)
}

func watch(conn *dbus.Conn, out io.Writer) error {
	w := &watcher{out: out}
	if err := conn.Export(w, watcherPath, watcherName); err != nil {
		return err
	}
	reply, err := conn.RequestName(watcherName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("%s is already owned", watcherName)
	}
	if err := conn.AddMatchSignal(dbus.WithMatchInterface(itemIface), dbus.WithMatchMember("XAyatanaNewLabel")); err != nil {
		return err
	}
	ch := make(chan *dbus.Signal, 64)
	conn.Signal(ch)
	w.print("ready\n")
	for sig := range ch {
		if line := signalLine(sig); line != "" {
			w.print(line)
		}
	}
	return nil
}

func get(conn *dbus.Conn, service, name string) (string, error) {
	v, err := conn.Object(service, itemPath).GetProperty(itemIface + "." + name)
	if err != nil {
		return "", err
	}
	if s, ok := v.Value().(string); ok {
		return s, nil
	}
	return fmt.Sprint(v.Value()), nil
}

func main() {
	usage := func() {
		fmt.Fprintln(os.Stderr, "usage: fakewatcher watch | fakewatcher get SERVICE PROPERTY")
		os.Exit(2)
	}
	if len(os.Args) < 2 {
		usage()
	}
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakewatcher:", err)
		os.Exit(1)
	}
	defer conn.Close()
	switch {
	case os.Args[1] == "watch" && len(os.Args) == 2:
		err = watch(conn, os.Stdout)
	case os.Args[1] == "get" && len(os.Args) == 4:
		var s string
		if s, err = get(conn, os.Args[2], os.Args[3]); err == nil {
			fmt.Println(s)
		}
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakewatcher:", err)
		os.Exit(1)
	}
}
