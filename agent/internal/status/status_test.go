package status

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/usage"
)

func pf(v float64) *float64 { return &v }

func TestWriteReadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "status.json")
	st := 200
	resets := time.Date(2026, 1, 9, 6, 0, 0, 0, time.UTC)
	s := Snapshot{Version: "0.0.0-dev", PID: 42, UpdatedAt: time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC),
		Mode: "coordinated", ServerURL: "http://ai-usage.local", LastSyncOK: true, Backlog: 3, SchemaFallback: true,
		Providers: map[provider.ID]ProviderStatus{provider.Claude: {State: "ok", Role: "poller", LastFetchStatus: &st, PeersPCs: 2,
			Rows:    []provider.Row{{Key: "weekly_all", Label: "Weekly", Percent: pf(5), ResetsAt: &resets, Level: usage.LevelNormal, WindowMins: 10080}},
			MainKey: "weekly_all"}}}
	if err := Write(p, s); err != nil {
		t.Fatal(err)
	}
	got, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, s) {
		t.Fatalf("got %+v", got)
	}
	raw, _ := os.ReadFile(p)
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	for _, k := range []string{`"mode":"coordinated"`, `"providers":{"claude":{"state":"ok"`, `"role":"poller"`, `"percent":5`,
		`"last_fetch_status":200`, `"updated_at":"2026-01-05T09:00:00Z"`, `"schema_fallback":true`, `"peers_pcs":2`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("missing %s in %s", k, b)
		}
	}
	if _, err := Read(filepath.Join(t.TempDir(), "none.json")); err == nil {
		t.Fatal("missing file should fail")
	}
}

func TestSnapshotHasNoSecretFields(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeOf(Snapshot{}), reflect.TypeOf(ProviderStatus{}), reflect.TypeOf(provider.Row{})} {
		for i := 0; i < typ.NumField(); i++ {
			n := strings.ToLower(typ.Field(i).Name + " " + typ.Field(i).Tag.Get("json"))
			if strings.Contains(n, "token") || strings.Contains(n, "ingest") || strings.Contains(n, "secret") {
				t.Errorf("%s field %s", typ.Name(), typ.Field(i).Name)
			}
		}
	}
	b, _ := json.Marshal(Snapshot{})
	if !strings.Contains(string(b), `"mode"`) {
		t.Fatal(string(b))
	}
}

func TestTrayFieldsRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "status.json")
	s := Snapshot{Mode: "coordinated", ClientMode: "tray", FreshPending: true}
	if err := Write(p, s); err != nil {
		t.Fatal(err)
	}
	got, err := Read(p)
	if err != nil || !reflect.DeepEqual(got, s) {
		t.Fatalf("got %+v, %v", got, err)
	}
	raw, _ := os.ReadFile(p)
	var buf bytes.Buffer
	_ = json.Compact(&buf, raw)
	for _, k := range []string{`"client_mode":"tray"`, `"fresh_pending":true`} {
		if !strings.Contains(buf.String(), k) {
			t.Errorf("missing %s in %s", k, buf.String())
		}
	}
	// empty values are left out, fresh_pending is always written
	if err := Write(p, Snapshot{Mode: "starting"}); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(p)
	for _, k := range []string{"schema_fallback", "server_url", "next_standalone_fetch"} {
		if strings.Contains(string(raw), k) {
			t.Errorf("%s written when empty", k)
		}
	}
	if !strings.Contains(string(raw), `"fresh_pending": false`) {
		t.Errorf("fresh_pending missing: %s", raw)
	}
}

// The status file a v0.8.1 agent wrote (testdata from the v0.8.1 code)
// reads with its Claude numbers under Providers["claude"]: an upgraded
// PC's tray shows them before the first sync.
func TestReadsAV081StatusFile(t *testing.T) {
	s, err := Read("../testdata/v081/status.json")
	if err != nil {
		t.Fatal(err)
	}
	if s.Version != "0.8.1" || s.Mode != "coordinated" || s.ClientMode != "tray" || !s.FreshPending || s.Backlog != 2 || s.ClientLatestVersion != "0.8.1" {
		t.Fatalf("top level %+v", s)
	}
	c := s.Provider(provider.Claude)
	at := func(v string) time.Time { x, _ := time.Parse(time.RFC3339, v); return x }
	if c.State != "ok" || c.Role != "standby" || c.AccountEmail != "user-a@example.com" || c.PeersPCs != 3 ||
		c.PollerHostname != "TEST-PC-03" || !c.ExpiresAt.Equal(at("2026-11-24T17:00:00Z")) ||
		!c.LatestFetchedAt.Equal(at("2026-11-24T08:58:00Z")) || *c.LastFetchStatus != 200 || c.MainKey != "weekly_all" {
		t.Fatalf("claude %+v", c)
	}
	w, ok := c.Row("weekly_all")
	if !ok || *w.Percent != 30 || w.Level != usage.LevelNormal || !w.ResetsAt.Equal(at("2026-11-28T16:00:00Z")) {
		t.Fatalf("weekly %+v", w)
	}
	f, ok := c.Row("weekly_scoped:Fable")
	if !ok || *f.Percent != 76 || f.Level != usage.LevelWarning || f.Label != "Fable" {
		t.Fatalf("fable %+v", f)
	}
	// a new file is read as it is, never through the old fields
	p := filepath.Join(t.TempDir(), "status.json")
	if err := Write(p, Snapshot{Mode: "starting", Providers: map[provider.ID]ProviderStatus{}}); err != nil {
		t.Fatal(err)
	}
	if n, _ := Read(p); len(n.Providers) != 0 {
		t.Fatalf("%+v", n)
	}
}
