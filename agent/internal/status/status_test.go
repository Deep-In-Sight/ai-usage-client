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
)

func TestWriteReadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "status.json")
	pct := 5.0
	st := 200
	s := Snapshot{Version: "0.0.0-dev", PID: 42, UpdatedAt: time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC),
		Mode: "coordinated", Role: "poller", ServerURL: "http://ai-usage.local", LastSyncOK: true,
		LastFetchStatus: &st, WeeklyPercent: &pct, Backlog: 3}
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
	for _, k := range []string{`"mode":"coordinated"`, `"role":"poller"`, `"weekly_percent":5`, `"last_fetch_status":200`, `"updated_at":"2026-01-05T09:00:00Z"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("missing %s in %s", k, b)
		}
	}
	if _, err := Read(filepath.Join(t.TempDir(), "none.json")); err == nil {
		t.Fatal("missing file should fail")
	}
}

func TestSnapshotHasNoSecretFields(t *testing.T) {
	typ := reflect.TypeOf(Snapshot{})
	for i := 0; i < typ.NumField(); i++ {
		n := strings.ToLower(typ.Field(i).Name + " " + typ.Field(i).Tag.Get("json"))
		if strings.Contains(n, "key") || (strings.Contains(n, "token") && !strings.Contains(n, "token_expires_at")) {
			t.Errorf("field %s", typ.Field(i).Name)
		}
	}
	b, _ := json.Marshal(Snapshot{})
	if !strings.Contains(string(b), `"mode"`) {
		t.Fatal(string(b))
	}
}

func TestTrayFieldsRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "status.json")
	s := Snapshot{Mode: "coordinated", ClientMode: "tray", WeeklySeverity: "warning", ScopedSeverity: "normal",
		ScopedResetsAt: time.Date(2026, 1, 9, 6, 0, 0, 0, time.UTC), FreshPending: true}
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
	for _, k := range []string{`"client_mode":"tray"`, `"weekly_severity":"warning"`, `"scoped_resets_at":"2026-01-09T06:00:00Z"`, `"scoped_severity":"normal"`, `"fresh_pending":true`} {
		if !strings.Contains(buf.String(), k) {
			t.Errorf("missing %s in %s", k, buf.String())
		}
	}
	// empty values are left out, fresh_pending is always written
	if err := Write(p, Snapshot{Mode: "starting"}); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(p)
	for _, k := range []string{"weekly_severity", "scoped_resets_at", "scoped_severity"} {
		if strings.Contains(string(raw), k) {
			t.Errorf("%s written when empty", k)
		}
	}
	if !strings.Contains(string(raw), `"fresh_pending": false`) {
		t.Errorf("fresh_pending missing: %s", raw)
	}
}
