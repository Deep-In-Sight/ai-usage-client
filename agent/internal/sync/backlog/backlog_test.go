package backlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/sync/protocol"
)

var now = time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC)

func eq[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func res(at time.Time) protocol.Result {
	return protocol.Result{AccountUUID: "acc-1", FetchedAt: protocol.Time{Time: at}, HTTPStatus: 200, Usage: json.RawMessage(`{"seven_day":{"utilization":5}}`)}
}

func times(rs []protocol.Result) []time.Time {
	var out []time.Time
	for _, r := range rs {
		out = append(out, r.FetchedAt.Time)
	}
	return out
}

func open(t *testing.T) (*Store, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "backlog.json")
	s, err := Open(p)
	must(t, err)
	return s, p
}

func TestAddPendingOrder(t *testing.T) {
	s, _ := open(t)
	t1, t2, t3 := now.Add(-3*time.Hour), now.Add(-2*time.Hour), now.Add(-time.Hour)
	must(t, s.Add(res(t3)))
	must(t, s.Add(res(t1)))
	must(t, s.Add(res(t2)))
	eq(t, times(s.Pending(10)), []time.Time{t1, t2, t3})
	eq(t, times(s.Pending(2)), []time.Time{t1, t2})
	// Pending returns copies
	p := s.Pending(1)
	p[0].HTTPStatus = 999
	eq(t, s.Pending(1)[0].HTTPStatus, 200)
}

func TestAddIgnoresDuplicate(t *testing.T) {
	s, _ := open(t)
	must(t, s.Add(res(now)))
	must(t, s.Add(res(now)))
	eq(t, s.Len(), 1)
	other := res(now)
	other.AccountUUID = "acc-2"
	must(t, s.Add(other))
	eq(t, s.Len(), 2)
}

func TestRemoveOnlySent(t *testing.T) {
	s, _ := open(t)
	var all []time.Time
	for i := 1; i <= 5; i++ {
		at := now.Add(time.Duration(i) * time.Minute)
		all = append(all, at)
		must(t, s.Add(res(at)))
	}
	must(t, s.Remove(s.Pending(3)))
	eq(t, s.Len(), 2)
	eq(t, times(s.Pending(10)), all[3:])
}

// seeded writes n results spaced by gap (the newest at now) straight to a
// file, newest first, and opens it (1100 Adds would each rewrite the file).
func seeded(t *testing.T, n int, gap time.Duration) (*Store, string) {
	t.Helper()
	var rs []protocol.Result
	for i := 0; i < n; i++ {
		rs = append(rs, res(now.Add(-time.Duration(i)*gap)))
	}
	b, err := json.Marshal(rs)
	must(t, err)
	p := filepath.Join(t.TempDir(), "backlog.json")
	must(t, os.WriteFile(p, b, 0o600))
	s, err := Open(p)
	must(t, err)
	eq(t, s.Len(), n)
	return s, p
}

func TestPruneAgeAndCap(t *testing.T) {
	s, p := seeded(t, 1100, 30*time.Minute)
	must(t, s.Prune(now))
	eq(t, s.Len(), 7*48+1) // age rule: 337 are within 7 days
	eq(t, s.Pending(1)[0].FetchedAt.Time, now.Add(-MaxAge))
	s2, err := Open(p) // the pruned state was saved
	must(t, err)
	eq(t, s2.Len(), 337)

	s, _ = seeded(t, 1100, time.Minute)
	must(t, s.Prune(now))
	eq(t, s.Len(), MaxItems) // cap rule: the newest 1000
	eq(t, s.Pending(1)[0].FetchedAt.Time, now.Add(-999*time.Minute))
	eq(t, s.Pending(MaxItems)[MaxItems-1].FetchedAt.Time, now)
}

func TestAddKeepsOrderAcrossMany(t *testing.T) {
	s, _ := seeded(t, 50, time.Minute)
	must(t, s.Add(res(now.Add(-90*time.Second))))
	ts := times(s.Pending(100))
	for i := 1; i < len(ts); i++ {
		if !ts[i-1].Before(ts[i]) {
			t.Fatalf("order broken at %d", i)
		}
	}
	eq(t, len(ts), 51)
}

func TestReopenKeepsItems(t *testing.T) {
	s, p := open(t)
	for i := 0; i < 3; i++ {
		must(t, s.Add(res(now.Add(time.Duration(i)*time.Minute))))
	}
	s2, err := Open(p)
	must(t, err)
	eq(t, s2.Len(), 3)
	eq(t, times(s2.Pending(10)), times(s.Pending(10)))
	eq(t, string(s2.Pending(1)[0].Usage), `{"seven_day":{"utilization":5}}`)
	if runtime.GOOS != "windows" {
		st, err := os.Stat(p)
		must(t, err)
		eq(t, st.Mode().Perm(), os.FileMode(0o600))
	}
}

func TestCorruptFileStartsEmpty(t *testing.T) {
	p := filepath.Join(t.TempDir(), "backlog.json")
	must(t, os.WriteFile(p, []byte("{not json"), 0o600))
	s, err := Open(p)
	must(t, err)
	eq(t, s.Len(), 0)
	if _, err := os.Stat(p + ".bad"); err != nil {
		t.Fatal(err)
	}
	must(t, s.Add(res(now)))
	eq(t, s.Len(), 1)
}
