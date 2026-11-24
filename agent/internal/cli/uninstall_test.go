package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/config"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/paths"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/engine"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/protocol"
)

// Removing tells the server once (design D8 rule 4): a schema 2 sync with
// event "uninstall", the device and the machine key, no provider blocks,
// sent while config.json is still there. A server that never answers
// holds removal up for 3 s at most, and removal goes on.
func TestUninstallNotifiesServer(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	must(t, os.WriteFile(te.Dirs.File(paths.DeviceIDFile), []byte("dev-uninstall-1\n"), 0o600))
	eq(t, te.main("uninstall"), 0)
	var events []protocol.RequestV2
	for _, r := range fs.v2requests() {
		if r.Event != nil {
			events = append(events, r)
		}
	}
	eq(t, len(events), 1)
	e := events[0]
	eq(t, *e.Event, "uninstall")
	eq(t, e.Device.ID, "dev-uninstall-1")
	eq(t, *e.Machine, protocol.MachineInfo{Key: fakeMachineKey, Source: "os"})
	eq(t, len(e.Providers), 0)
	if _, err := os.Stat(te.Dirs.Data); !os.IsNotExist(err) {
		t.Fatal("data still there")
	}

	hang := make(chan struct{})
	silent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-hang }))
	defer func() { close(hang); silent.Close() }()
	te = trayEnv(t, fs)
	must(t, config.Save(te.Dirs.File(paths.ConfigFile), mustParse(t, `{"server_urls":["`+silent.URL+`"],"ingest_key":"test-ingest-key-0123456789"}`)))
	te.Syncer = func(cfg config.Config, ua string) engine.Syncer {
		return onlyTo{base: silent.URL, next: protocol.NewClient(cfg.IngestKey, ua, nil)}
	}
	must(t, os.WriteFile(te.Dirs.File(paths.DeviceIDFile), []byte("dev-uninstall-2\n"), 0o600))
	start := time.Now()
	eq(t, te.main("uninstall"), 0)
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("removal took %v with a silent server", d)
	}
	if _, err := os.Stat(te.Dirs.Data); !os.IsNotExist(err) {
		t.Fatal("data still there")
	}
	// the failure is said once and does not fail the removal
	if n := strings.Count(te.err.String(), "could not tell the server"); n != 1 {
		t.Fatalf("stderr %q", te.err.String())
	}
}

// Without a device id there is nothing to retire: no request.
func TestUninstallWithoutDeviceSendsNothing(t *testing.T) {
	fs := newFakeServer(t)
	te := trayEnv(t, fs)
	eq(t, te.main("uninstall"), 0)
	eq(t, len(fs.v2requests()), 0)
}

// A server without schema 2: status says the agent speaks schema 1, Claude
// only.
func TestStatusShowsSchemaFallback(t *testing.T) {
	te := newEnv(t)
	must(t, status.Write(te.Dirs.File(paths.StatusFile), status.Snapshot{Mode: "coordinated", SchemaFallback: true}))
	eq(t, te.main("status"), 0)
	contains(t, te.out.String(), "Last sync:  never failing\nProtocol:   schema 1, Claude only (the server does not support schema 2 yet)\n")
	must(t, status.Write(te.Dirs.File(paths.StatusFile), status.Snapshot{Mode: "coordinated"}))
	te.out.Reset()
	eq(t, te.main("status"), 0)
	if strings.Contains(te.out.String(), "Protocol:") {
		t.Fatal(te.out.String())
	}
}
