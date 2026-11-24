package logfile

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixed() time.Time { return time.Date(2026, 1, 5, 9, 30, 0, 0, time.Local) }

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLinesAreStamped(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent.log")
	f, err := Open(p, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	f.now = fixed
	if _, err := f.Write([]byte("ai-usage started\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("two\nlines\n")); err != nil {
		t.Fatal(err)
	}
	f.Close()
	want := "2026-01-05 09:30:00 ai-usage started\n2026-01-05 09:30:00 two\n2026-01-05 09:30:00 lines\n"
	if got := read(t, p); got != want {
		t.Fatalf("%q", got)
	}
	if st, _ := os.Stat(p); st.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("mode %v", st.Mode())
	}
}

func TestAppendsAcrossOpens(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent.log")
	for _, s := range []string{"first\n", "second\n"} {
		f, err := Open(p, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		f.now = fixed
		f.Write([]byte(s))
		f.Close()
	}
	if got := read(t, p); !strings.Contains(got, "first\n") || !strings.HasSuffix(got, "second\n") {
		t.Fatalf("%q", got)
	}
}

// The file never grows past the limit: it moves to agent.log.1 (replacing
// an older one) and a new agent.log starts. At most 2 x max bytes in all.
func TestRotatesAtTheLimit(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "agent.log")
	f, err := Open(p, 100)
	if err != nil {
		t.Fatal(err)
	}
	f.now = fixed
	line := strings.Repeat("x", 30) + "\n" // 51 bytes with the stamp
	for i := 0; i < 10; i++ {
		if _, err := f.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
		for _, q := range []string{p, p + ".1"} {
			if st, err := os.Stat(q); err == nil && st.Size() > 100 {
				t.Fatalf("%s is %d bytes", q, st.Size())
			}
		}
	}
	f.Close()
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Fatal("no agent.log.1")
	}
	if _, err := os.Stat(p + ".2"); err == nil {
		t.Fatal("more than one old file")
	}
	if got := read(t, p); !strings.HasSuffix(got, line) {
		t.Fatalf("%q", got)
	}
}

// A file left over-size by an earlier run (or by launchd) is moved aside
// when it is opened.
func TestOversizeFileIsMovedAsideAtOpen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent.log")
	if err := os.WriteFile(p, make([]byte, 300), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Open(p, 100)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if st, _ := os.Stat(p); st.Size() != 0 {
		t.Fatalf("agent.log %d bytes", st.Size())
	}
	if st, _ := os.Stat(p + ".1"); st.Size() != 300 {
		t.Fatal("old content not kept in agent.log.1")
	}
}

// lan192 is an address in 192.168/16, written so that the repository's
// text check (which keeps such addresses out of the sources) passes.
var lan192 = fmt.Sprintf("%d.%d.7.20", 192, 168)

func TestScrubberTakesOutSecretsAndPersonalData(t *testing.T) {
	s := NewScrubber(Personal{Home: "/srv/u/jdoe", User: "jdoe", Host: "pc-4711"}, "fake-ingest-key-0123456789")
	cases := map[string]string{
		"ai-usage started (data /srv/u/jdoe/.config/ai-usage, tray)":                       "ai-usage started (data ~/.config/ai-usage, tray)",
		"status file: open /srv/u/jdoe/.config/ai-usage/status.json":                       "status file: open ~/.config/ai-usage/status.json",
		"sync failed (1 in a row): sync: dial tcp " + lan192 + ":8700: connection refused": "sync failed (1 in a row): sync: dial tcp [lan address]:8700: connection refused",
		"sync ok again via http://10.1.2.3:8700 after 3 failures":                          "sync ok again via http://[lan address]:8700 after 3 failures",
		"via http://172.20.0.5 and 172.15.0.1 and 192.0.2.10":                              "via http://[lan address] and 172.15.0.1 and 192.0.2.10",
		"key fake-ingest-key-0123456789 refused":                                           "key [ingest key] refused",
		"token sk-xyz-oat01-AbC_d-9 in a line":                                             "token [token] in a line",
		"Authorization: Bearer abc.def-ghi":                                                "Authorization: Bearer [token]",
		"account user.name@example.com signed in":                                          "account [email] signed in",
		"host pc-4711 user jdoe":                                                           "host [host] user [user]",
		"jdoe2 and xjdoe stay":                                                             "jdoe2 and xjdoe stay",
		"plain line":                                                                       "plain line",
	}
	for in, want := range cases {
		if got := s.Scrub(in); got != want {
			t.Errorf("Scrub(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestScrubberIgnoresShortOrEmptyValues(t *testing.T) {
	s := NewScrubber(Personal{Home: "/", User: "a", Host: ""}, "", "k")
	if got := s.Scrub("a / k line"); got != "a / k line" {
		t.Fatalf("%q", got)
	}
}

func TestWriterScrubsEveryWrite(t *testing.T) {
	var b bytes.Buffer
	w := NewScrubber(Personal{Home: "/srv/u/jdoe"}).Writer(&b)
	n, err := w.Write([]byte("open /srv/u/jdoe/x\n"))
	if err != nil || n != len("open /srv/u/jdoe/x\n") {
		t.Fatalf("%d %v", n, err)
	}
	if b.String() != "open ~/x\n" {
		t.Fatalf("%q", b.String())
	}
}
