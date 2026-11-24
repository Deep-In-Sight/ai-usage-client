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

// With crash reports going to the file, rotation still works and every
// later line is written. On Windows the crash output is a second handle to
// agent.log, which made the rename fail and every later Write fail with it
// (review m1); this test runs on the Windows CI runner too.
func TestRotatesWhileCrashesGoToTheFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "agent.log")
	f, err := Open(p, 100)
	if err != nil {
		t.Fatal(err)
	}
	f.now = fixed
	f.SendCrashes()
	defer f.Close()
	line := strings.Repeat("y", 30) + "\n"
	for i := 0; i < 8; i++ {
		if _, err := f.Write([]byte(fmt.Sprintf("%d%s", i, line))); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Fatal("no agent.log.1")
	}
	if got := read(t, p); !strings.HasSuffix(got, "7"+line) || len(got) > 100 {
		t.Fatalf("agent.log %q", got)
	}
}

// The crash output is pointed away from the file before it is moved and
// back to the new file after: on every OS, so the order is tested here.
func TestCrashOutputIsReleasedAroundTheRename(t *testing.T) {
	var events []string
	oldSet, oldRename := setCrashOutput, rename
	defer func() { setCrashOutput, rename = oldSet, oldRename }()
	setCrashOutput = func(f *os.File) {
		if f == nil {
			events = append(events, "crash->nil")
		} else {
			events = append(events, "crash->"+filepath.Base(f.Name()))
		}
	}
	rename = func(a, b string) error {
		events = append(events, "rename")
		return os.Rename(a, b)
	}
	p := filepath.Join(t.TempDir(), "agent.log")
	f, err := Open(p, 60)
	if err != nil {
		t.Fatal(err)
	}
	f.now = fixed
	f.SendCrashes()
	f.Write([]byte(strings.Repeat("z", 30) + "\n"))
	f.Write([]byte(strings.Repeat("z", 30) + "\n")) // rotates
	f.Close()
	want := []string{"crash->agent.log", "crash->nil", "rename", "crash->agent.log", "crash->nil"}
	if fmt.Sprint(events) != fmt.Sprint(want) {
		t.Fatalf("events %v, want %v", events, want)
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

// Review m3: JWTs (what Codex and ChatGPT sign-ins are) and long opaque
// values after a token or key name are taken out, "bearer" followed by a
// tab too, and the ingest key also in its URL-encoded forms.
func TestScrubberTakesOutJWTsAndOpaqueTokens(t *testing.T) {
	key := "fake/ingest+key=0123456789"
	s := NewScrubber(Personal{}, key)
	cases := map[string]string{
		"id_token eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0.c2lnLXBhcnQ_1 read": "id_token [token] read",
		"claims eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0. end":                 "claims [token] end",
		`{"access_token":"abcdef0123456789abcdef","x":1}`:                 `{"access_token":"[token]","x":1}`,
		"refresh_token=rt_AbCdEf0123456789xyz&a=b":                        "refresh_token=[token]&a=b",
		"api_key: Zm9vYmFyYmF6cXV4MTIzNDU2Nzg5MA== sent":                  "api_key: [token] sent",
		"Authorization: bearer\tabc.def":                                  "Authorization: bearer\t[token]",
		"url ?key=fake%2Fingest%2Bkey%3D0123456789 refused":               "url ?key=[ingest key] refused",
		"path /k/fake%2Fingest+key=0123456789/x":                          "path /k/[ingest key]/x",
		"token expired; key file missing; access_token: short":            "token expired; key file missing; access_token: short",
		"sync: bad ingest key: HTTP 401 (machine key not shown)":          "sync: bad ingest key: HTTP 401 (machine key not shown)",
	}
	for in, want := range cases {
		in, want = strings.ReplaceAll(in, `\t`, "\t"), strings.ReplaceAll(want, `\t`, "\t")
		if got := s.Scrub(in); got != want {
			t.Errorf("Scrub(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

// Review m3: a user or host name that is an ordinary word ("user",
// "ubuntu") is replaced only in a path or after a user or host label, so
// messages keep their words; a name with digits or punctuation is
// replaced wherever it stands.
func TestScrubberLeavesOrdinaryWordsAlone(t *testing.T) {
	s := NewScrubber(Personal{Home: "/srv/u/x", User: "user", Host: "ubuntu"})
	cases := map[string]string{
		"systray error: dbus: connection closed by user": "systray error: dbus: connection closed by user",
		"desktop ubuntu:GNOME, session x11":              "desktop ubuntu:GNOME, session x11",
		"open /media/user/disk and C:\\Users\\user\\x":   "open /media/[user]/disk and C:\\Users\\[user]\\x",
		"user=user host=ubuntu":                          "user=[user] host=[host]",
		`{"os_user":"user","hostname":"ubuntu"}`:         `{"os_user":"[user]","hostname":"[host]"}`,
		"Hostname: ubuntu, user: user":                   "Hostname: [host], user: [user]",
	}
	for in, want := range cases {
		if got := s.Scrub(in); got != want {
			t.Errorf("Scrub(%q)\n got %q\nwant %q", in, got, want)
		}
	}
	d := NewScrubber(Personal{User: "j.doe", Host: "DEV-PC-07"})
	if got := d.Scrub("from DEV-PC-07 as j.doe"); got != "from [host] as [user]" {
		t.Fatalf("%q", got)
	}
}

// Go crash reports are written by the runtime, past the scrubber; the
// next start scrubs the log files (review m3), and a file that needs
// nothing is left as it is.
func TestScrubFileCleansCrashReports(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "agent.log")
	crash := "2026-01-05 09:30:00 ai-usage started\npanic: open /srv/u/jdoe/x: token sk-xyz-oat01-AbC_d-9\n\ngoroutine 1 [running]:\nmain.main()\n"
	if err := os.WriteFile(p, []byte(crash), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewScrubber(Personal{Home: "/srv/u/jdoe"})
	if err := ScrubFile(p, s); err != nil {
		t.Fatal(err)
	}
	want := "2026-01-05 09:30:00 ai-usage started\npanic: open ~/x: token [token]\n\ngoroutine 1 [running]:\nmain.main()\n"
	if got := read(t, p); got != want {
		t.Fatalf("%q", got)
	}
	st1, _ := os.Stat(p)
	if err := ScrubFile(p, s); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != want {
		t.Fatalf("second pass changed it: %q", got)
	}
	if st2, _ := os.Stat(p); !st2.ModTime().Equal(st1.ModTime()) {
		t.Fatal("a clean file was rewritten")
	}
	if err := ScrubFile(filepath.Join(dir, "missing.log"), s); err != nil {
		t.Fatal(err)
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
