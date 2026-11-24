package creds

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sharedread"
)

// Options say where the sign-in lives.
type Options struct {
	Home            string
	ClaudeConfigDir string // from config.json; "" means <Home>/.claude
	KeychainService string // "" means KeychainService(ClaudeConfigDir)
	UseKeychain     bool   // macOS: try the keychain before the file
	Keychain        KeychainFunc
	Clock           clock.Clock
}

// Read caps: the credentials file is small; .claude.json can be large.
const maxCredsFile = 1 << 20

// maxAccountFile is a var so that tests can lower it.
var maxAccountFile int64 = 32 << 20

// capText is a byte count for a Problem line, e.g. "32 MiB".
func capText(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 1<<10 && n%(1<<10) == 0:
		return fmt.Sprintf("%d KiB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

// readCapped reads at most limit bytes of one of Claude Code's files,
// opened read-only and shared fully (sharedread: on Windows Claude Code can
// still replace it by rename while it is open, review I-1).
func readCapped(path string, limit int64) ([]byte, error) { return sharedread.ReadFile(path, limit) }

// macReloginGap limits keychain re-reads triggered by .claude.json changes.
const macReloginGap = 10 * time.Minute

// badReadsToGiveUp is how many unreadable credential files in a row are
// needed before the last good sign-in is dropped (Claude Code may be in the
// middle of rewriting the file).
const badReadsToGiveUp = 3

type stamp struct {
	exists bool
	mod    time.Time
	size   int64
}

func (a stamp) same(b stamp) bool {
	return a.exists == b.exists && a.mod.Equal(b.mod) && a.size == b.size
}

func stampOf(path string) stamp {
	st, err := os.Stat(path)
	if err != nil {
		return stamp{}
	}
	return stamp{exists: true, mod: st.ModTime(), size: st.Size()}
}

// Reader reads the sign-in under the read-timing rule: at start, when the
// credentials file changes, once when a known expiry passes, and for every
// fetch. It never keeps the token.
type Reader struct {
	o Options

	mu         sync.Mutex
	reads      int
	have       bool
	login      Login
	good       Login
	haveGood   bool
	bad        int
	retry      bool
	credStamp  stamp
	acctStamp  stamp
	expiryDone time.Time
	lastRead   time.Time
}

// NewReader returns a Reader. It reads nothing yet.
func NewReader(o Options) *Reader {
	if o.Clock == nil {
		o.Clock = clock.Real{}
	}
	if o.Keychain == nil {
		o.Keychain = ReadKeychain
	}
	if o.KeychainService == "" {
		o.KeychainService = KeychainService(o.ClaudeConfigDir)
	}
	return &Reader{o: o}
}

func (r *Reader) credPath() string {
	if r.o.ClaudeConfigDir != "" {
		return filepath.Join(r.o.ClaudeConfigDir, ".credentials.json")
	}
	return filepath.Join(r.o.Home, ".claude", ".credentials.json")
}

func (r *Reader) accountPath() string {
	if r.o.ClaudeConfigDir != "" {
		return filepath.Join(r.o.ClaudeConfigDir, ".claude.json")
	}
	return filepath.Join(r.o.Home, ".claude.json")
}

// Reads is how many times the sign-in was actually read (for tests).
func (r *Reader) Reads() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reads
}

// Current returns the known sign-in, reading it again only when the rule
// says so.
func (r *Reader) Current(ctx context.Context) Login {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.o.Clock.Now()
	if r.needRead(now) {
		r.read(ctx, now)
	}
	return r.login
}

func (r *Reader) needRead(now time.Time) bool {
	if !r.have || r.retry {
		return true
	}
	if !stampOf(r.credPath()).same(r.credStamp) {
		return true
	}
	if exp := r.login.ExpiresAt; !exp.IsZero() && !now.Before(exp) && !r.expiryDone.Equal(exp) {
		return true
	}
	// The keychain has no modification time. When the known sign-in is not
	// usable, a change to .claude.json (Claude Code writes it on sign-in)
	// triggers a re-read, at most every macReloginGap.
	if r.o.UseKeychain && (r.login.Source == "keychain" || !r.login.HasToken) &&
		r.login.State(now) != "ok" && now.Sub(r.lastRead) >= macReloginGap &&
		!stampOf(r.accountPath()).same(r.acctStamp) {
		return true
	}
	return false
}

// read reads the sign-in and stores the Login. The token is returned to the
// caller only; it is "" when no fresh token was read.
func (r *Reader) read(ctx context.Context, now time.Time) (Login, string) {
	r.reads++
	r.lastRead = now
	r.credStamp = stampOf(r.credPath())
	r.acctStamp = stampOf(r.accountPath())

	var l Login
	var token string
	var perr error
	got := false
	kcProblem := ""
	if r.o.UseKeychain {
		b, err := r.o.Keychain(ctx, r.o.KeychainService)
		if err == nil {
			if kl, tok, e := parseCreds(b); e == nil {
				l, token, got = kl, tok, true
				l.Source = "keychain"
			} else {
				kcProblem = "keychain item: " + e.Error()
			}
		} else {
			kcProblem = err.Error()
		}
	}
	if !got {
		b, err := readCapped(r.credPath(), maxCredsFile)
		switch {
		case errors.Is(err, os.ErrNotExist):
			l = Login{Problem: "no Claude Code sign-in found"}
		case err != nil:
			l = Login{Problem: "credentials file could not be read"}
		default:
			l, token, perr = parseCreds(b)
			if perr == nil {
				l.Source = "file"
			}
		}
		if kcProblem != "" && !l.HasToken {
			l.Problem = kcProblem + "; " + l.Problem
		}
	}

	if errors.Is(perr, errNotJSON) {
		r.bad++
		token = ""
		switch {
		case r.bad >= badReadsToGiveUp:
			r.retry = false
			l = Login{Problem: errNotJSON.Error()}
		case r.haveGood:
			r.retry = true
			l = r.good
		default:
			r.retry = true
			l = Login{Problem: errNotJSON.Error()}
		}
	} else {
		r.bad = 0
		r.retry = false
		if l.HasToken {
			if f, err := sharedread.Open(r.accountPath()); err == nil {
				// One byte over the cap tells a file that is too large from
				// one that is exactly the cap.
				lr := &io.LimitedReader{R: f, N: maxAccountFile + 1}
				l.Account = parseAccountFrom(lr)
				f.Close()
				if l.Account == nil && lr.N == 0 {
					l.Problem = "account file larger than " + capText(maxAccountFile)
				}
			}
		}
	}
	if l.HasToken && token != "" {
		r.good, r.haveGood = l, true
	}
	if !l.ExpiresAt.IsZero() && !now.Before(l.ExpiresAt) {
		r.expiryDone = l.ExpiresAt
	}
	r.login, r.have = l, true
	return l, token
}

// WithToken reads the sign-in and, when it is usable, calls fn with the
// access token. fn must not keep the token. Without a usable sign-in fn is
// not called and the error wraps ErrNotSignedIn or ErrExpired.
func (r *Reader) WithToken(ctx context.Context, fn func(token string) error) error {
	r.mu.Lock()
	now := r.o.Clock.Now()
	l, token := r.read(ctx, now)
	r.mu.Unlock()
	switch {
	case !l.HasToken:
		return fmt.Errorf("%w (%s)", ErrNotSignedIn, l.Problem)
	case token == "":
		return fmt.Errorf("%w (%s)", ErrNotSignedIn, errNotJSON.Error())
	case l.State(now) == "expired":
		return ErrExpired
	}
	return fn(token)
}
