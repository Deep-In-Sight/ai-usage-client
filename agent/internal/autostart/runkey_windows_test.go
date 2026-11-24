package autostart

import (
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/windows/registry"
)

func TestRunKeyRoundTrip(t *testing.T) {
	name := fmt.Sprintf("AIUsageTest-%d", os.Getpid())
	var started [][]string
	r := &RunKey{ValueName: name, Start: func(exe string, args ...string) error {
		started = append(started, append([]string{exe}, args...))
		return nil
	}}
	t.Cleanup(func() { _ = r.Unregister(bg) })
	exe := `C:\Users\a b\AppData\Local\Programs\AIUsage\ai-usage.exe`
	eq(t, r.Name(), "hkcu-run")
	eq(t, r.State(bg), "not registered")
	must(t, r.Register(bg, exe))
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	must(t, err)
	v, _, err := k.GetStringValue(name)
	k.Close()
	must(t, err)
	eq(t, v, RunValue(exe))
	eq(t, started, [][]string{{exe, "run"}})
	eq(t, r.State(bg), "registered")
	eq(t, r.Enabled(bg), true)
	must(t, r.SetEnabled(bg, exe, false))
	eq(t, r.Enabled(bg), false)
	must(t, r.Unregister(bg))
	must(t, r.Unregister(bg))
	eq(t, len(started), 1)
}

func TestPreflightRunKey(t *testing.T) {
	must(t, (&RunKey{}).Preflight(bg))
}
