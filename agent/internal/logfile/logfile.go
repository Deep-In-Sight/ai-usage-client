// Package logfile is the agent's log file: agent.log in the data folder,
// each line stamped with the local time, moved to agent.log.1 when it would
// grow past a limit (one older file is kept), and a scrubber that takes
// secrets and personal details out of every line before it is written.
package logfile

import (
	"errors"
	"io"
	"net/url"
	"os"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"
)

// Max is the size at which agent.log is moved to agent.log.1.
const Max = 1 << 20

// Seams for tests: where Go crash reports go, and moving a file.
var (
	setCrashOutput = func(f *os.File) { _ = debug.SetCrashOutput(f, debug.CrashOptions{}) }
	rename         = os.Rename
)

// File is an append-only log file that rotates at a size limit. It is safe
// for concurrent use.
type File struct {
	mu      sync.Mutex
	path    string
	max     int64
	f       *os.File
	size    int64
	midLine bool // the last write did not end a line
	crashes bool
	now     func() time.Time
}

// Open opens (or creates) path for appending. A file already larger than
// max is first moved to path+".1".
func Open(path string, max int64) (*File, error) {
	l := &File{path: path, max: max, now: time.Now}
	if st, err := os.Stat(path); err == nil && st.Size() > max {
		if err := l.moveAside(); err != nil {
			return nil, err
		}
	}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *File) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.f, l.size, l.midLine = f, st.Size(), false
	if l.crashes {
		setCrashOutput(f)
	}
	return nil
}

func (l *File) moveAside() error {
	old := l.path + ".1"
	_ = os.Remove(old) // Windows cannot rename over an existing file
	if err := rename(l.path, old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// SendCrashes makes the Go runtime also write a fatal error or an
// unrecovered panic (with its stack trace) to this file, from now on and
// after every rotation. Close undoes it.
func (l *File) SendCrashes() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.crashes = true
	if l.f != nil {
		setCrashOutput(l.f)
	}
}

// Write stamps each line and appends it, rotating first when the file
// would pass the limit.
func (l *File) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return 0, os.ErrClosed
	}
	out := l.stamp(p)
	if l.size > 0 && l.size+int64(len(out)) > l.max {
		// The crash output is a second handle to the file (on Windows one
		// that forbids moving it), so it lets go first; open points it at
		// the new file.
		if l.crashes {
			setCrashOutput(nil)
		}
		l.f.Close()
		l.f = nil
		if err := l.moveAside(); err != nil {
			_ = l.open()
			return 0, err
		}
		if err := l.open(); err != nil {
			return 0, err
		}
		out = l.stamp(p) // a new file starts at a line
	}
	n, err := l.f.Write(out)
	l.size += int64(n)
	if err != nil {
		return 0, err
	}
	l.midLine = len(p) > 0 && p[len(p)-1] != '\n'
	return len(p), nil
}

func (l *File) stamp(p []byte) []byte {
	ts := l.now().Format("2006-01-02 15:04:05 ")
	var b strings.Builder
	start := !l.midLine
	for len(p) > 0 {
		if start {
			b.WriteString(ts)
		}
		i := strings.IndexByte(string(p), '\n')
		if i < 0 {
			b.Write(p)
			break
		}
		b.Write(p[:i+1])
		p = p[i+1:]
		start = true
	}
	return []byte(b.String())
}

// Close closes the file and stops sending crashes to it.
func (l *File) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.crashes {
		setCrashOutput(nil)
		l.crashes = false
	}
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// Personal are details of this PC and user that a log line must not carry.
type Personal struct {
	Home string // the home folder, shown as ~
	User string // the OS user name
	Host string // the host name
}

// Scrubber replaces secrets and personal details in log text: the home
// folder with ~, the given secrets (the ingest key, also URL-encoded) with
// [ingest key], sign-in tokens (sk-…, JWTs, bearer values, long values
// after a token or key name) with [token], e-mail addresses with [email],
// private IPv4 addresses (10/8, 172.16/12, 192.168/16) with [lan address],
// and the host and user names with [host] and [user]. A name that is an
// ordinary word (letters only, such as "user" or "ubuntu") is replaced only
// in a path or after a user or host label, so that messages keep their
// words; other names wherever they stand as a word.
type Scrubber struct {
	homes   []string
	secrets []string
	words   []*regexp.Regexp
	repl    []string
}

var (
	// API keys and sign-in tokens of the form sk-<kind>-<value>
	tokenRE = regexp.MustCompile(`\bsk-[A-Za-z0-9]{2,8}-[A-Za-z0-9_-]{6,}`)
	// JSON Web Tokens (Codex and ChatGPT sign-ins are JWTs): header.payload.signature
	jwtRE    = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)
	bearerRE = regexp.MustCompile(`(?i)\b(bearer[ \t]+)\S+`)
	// a long opaque value after a name that ends in token, key, secret, …
	namedRE = regexp.MustCompile(`(?i)(\b[a-z0-9_-]*(?:token|key|secret|password|passwd|credential)s?["']?[ \t]*[:=][ \t]*["']?)` +
		`[A-Za-z0-9._~+/=-]{16,}`)
	emailRE = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	lanRE   = regexp.MustCompile(`\b(?:10\.\d{1,3}|192\.168|172\.(?:1[6-9]|2\d|3[01]))\.\d{1,3}\.\d{1,3}\b`)
	// letters only: could be an ordinary word
	wordLike = regexp.MustCompile(`^[A-Za-z]+$`)
)

// Labels before a user or a host name.
const (
	userLabels = `os_user|username|user|login|owner`
	hostLabels = `hostname|host|computername|computer|machine`
)

// NewScrubber makes a scrubber for this PC's details and the given
// secrets. Values too short to be told apart from ordinary words are
// left alone.
func NewScrubber(p Personal, secrets ...string) *Scrubber {
	s := &Scrubber{}
	if h := strings.TrimRight(p.Home, `/\`); len(h) > 1 {
		s.homes = append(s.homes, h)
		if alt := strings.ReplaceAll(h, `\`, "/"); alt != h {
			s.homes = append(s.homes, alt)
		}
	}
	for _, v := range secrets {
		if len(v) < 8 {
			continue
		}
		for _, form := range []string{v, url.QueryEscape(v), url.PathEscape(v)} {
			if !slices.Contains(s.secrets, form) {
				s.secrets = append(s.secrets, form)
			}
		}
	}
	// the longest first, so no form is left half replaced
	slices.SortStableFunc(s.secrets, func(a, b string) int { return len(b) - len(a) })
	for _, w := range []struct{ v, repl, labels string }{{p.Host, "[host]", hostLabels}, {p.User, "[user]", userLabels}} {
		if len(w.v) < 3 {
			continue
		}
		q := regexp.QuoteMeta(w.v)
		if wordLike.MatchString(w.v) {
			s.words = append(s.words, regexp.MustCompile(`([/\\])`+q+`\b`),
				regexp.MustCompile(`(\b(?i:`+w.labels+`)["']?[ \t]*[:=]?[ \t]*["']?)`+q+`\b`))
			s.repl = append(s.repl, "${1}"+w.repl, "${1}"+w.repl)
		} else {
			s.words = append(s.words, regexp.MustCompile(`\b`+q+`\b`))
			s.repl = append(s.repl, w.repl)
		}
	}
	return s
}

// Scrub returns s with secrets and personal details replaced.
func (s *Scrubber) Scrub(line string) string {
	for _, h := range s.homes {
		line = strings.ReplaceAll(line, h, "~")
	}
	for _, v := range s.secrets {
		line = strings.ReplaceAll(line, v, "[ingest key]")
	}
	line = tokenRE.ReplaceAllString(line, "[token]")
	line = jwtRE.ReplaceAllString(line, "[token]")
	line = bearerRE.ReplaceAllString(line, "${1}[token]")
	line = namedRE.ReplaceAllString(line, "${1}[token]")
	line = emailRE.ReplaceAllString(line, "[email]")
	line = lanRE.ReplaceAllString(line, "[lan address]")
	for i, re := range s.words {
		line = re.ReplaceAllString(line, s.repl[i])
	}
	return line
}

// maxScrubFile is the most of a log file ScrubFile reads: a log file is at
// most Max plus one crash report.
const maxScrubFile = 8 << 20

// ScrubFile scrubs a log file in place. Go crash reports reach agent.log
// from the runtime, past the scrubber, so a run scrubs the files the run
// before it left (review m3). A missing file, or one that needs nothing,
// is left alone. It rewrites the same file (not a new one), because under
// launchd standard error stays open on it.
func ScrubFile(path string, s *Scrubber) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	b, err := io.ReadAll(io.LimitReader(f, maxScrubFile+1))
	f.Close()
	if err != nil {
		return err
	}
	if len(b) > maxScrubFile {
		return errors.New("log file too large to scrub")
	}
	clean := s.Scrub(string(b))
	if clean == string(b) {
		return nil
	}
	return os.WriteFile(path, []byte(clean), 0o600)
}

// Writer returns a writer that scrubs each write before passing it to w.
// It reports len(p) as written when w succeeds.
func (s *Scrubber) Writer(w io.Writer) io.Writer { return scrubWriter{s, w} }

type scrubWriter struct {
	s *Scrubber
	w io.Writer
}

func (sw scrubWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(sw.w, sw.s.Scrub(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}
