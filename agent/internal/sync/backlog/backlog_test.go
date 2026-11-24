package backlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/protocol"
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

func res(at time.Time) Item {
	return Item{Provider: "claude", AccountKey: "acc-1", FetchedAt: protocol.Time{Time: at}, HTTPStatus: 200, Usage: json.RawMessage(`{"seven_day":{"utilization":5}}`)}
}

func times(rs []Item) []time.Time {
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
	other.AccountKey = "acc-2"
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
	var rs []Item
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

// A v0.8.1 backlog (testdata from the v0.8.1 code: items without a
// provider) reads as Claude results, and is written back with one.
func TestV081BacklogReadsAsClaude(t *testing.T) {
	b, err := os.ReadFile("../../testdata/v081/backlog.json")
	must(t, err)
	p := filepath.Join(t.TempDir(), "backlog.json")
	must(t, os.WriteFile(p, b, 0o600))
	s, err := Open(p)
	must(t, err)
	items := s.Pending(10)
	eq(t, len(items), 2)
	for _, it := range items {
		eq(t, it.Provider, "claude")
		eq(t, it.AccountKey, "acc-1")
	}
	eq(t, items[0].HTTPStatus, 200)
	eq(t, items[1].HTTPStatus, 429)
	eq(t, items[1].Usage == nil, true)
	eq(t, len(s.Pending(10, "claude")), 2)
	eq(t, len(s.Pending(10, "codex")), 0)
	later := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC) // after the fixture's items
	must(t, s.Add(Item{Provider: "codex", AccountKey: "codex:a:b", FetchedAt: protocol.Time{Time: later}, HTTPStatus: 200, Source: "wham"}))
	raw, err := os.ReadFile(p)
	must(t, err)
	var back []map[string]any
	must(t, json.Unmarshal(raw, &back))
	eq(t, back[0]["provider"], any("claude"))
	eq(t, back[0]["account_uuid"], any("acc-1")) // the v0.8 name on disk
	eq(t, back[2]["source"], any("wham"))
}

// Results are told apart by provider, account and time: a Claude and a
// Codex result of the same millisecond are both kept, and each provider
// keeps its own newest MaxItems.
func TestProviderIsPartOfTheKey(t *testing.T) {
	s, _ := open(t)
	c := res(now)
	x := res(now)
	x.Provider = "codex"
	must(t, s.Add(c))
	must(t, s.Add(x))
	must(t, s.Add(x))
	eq(t, s.Len(), 2)
	eq(t, len(s.Pending(10, "codex")), 1)
	must(t, s.Remove(s.Pending(10, "codex")))
	eq(t, s.Pending(10)[0].Provider, "claude")
	eq(t, s.Len(), 1)
}

func TestPruneCapsEachProvider(t *testing.T) {
	var rs []Item
	for i := 0; i < 1100; i++ {
		c := res(now.Add(-time.Duration(i) * time.Minute))
		x := c
		x.Provider = "codex"
		rs = append(rs, c, x)
	}
	b, err := json.Marshal(rs)
	must(t, err)
	p := filepath.Join(t.TempDir(), "backlog.json")
	must(t, os.WriteFile(p, b, 0o600))
	s, err := Open(p)
	must(t, err)
	must(t, s.Prune(now))
	eq(t, len(s.Pending(5000, "claude")), MaxItems)
	eq(t, len(s.Pending(5000, "codex")), MaxItems)
	eq(t, s.Pending(1, "codex")[0].FetchedAt.Time, now.Add(-999*time.Minute))
}
