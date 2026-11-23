// Package engine is the agent's loop: sync with the server, fetch when told
// to and sync again at once, and fall back to stand-alone fetching after
// three failed syncs. The coordination rules live on the server; Step is a
// deterministic function of its inputs and the (fake or real) clock.
//
// The engine sees the sign-in only as creds.Login (no token). Fetching goes
// through the Fetcher, which is the only path to the token.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math/rand/v2"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/anthropic"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/atomicfile"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/clock"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/config"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/creds"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/status"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/sync/backlog"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/sync/discover"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/sync/protocol"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/usage"
)

const (
	FailuresToStandalone = 3
	RetryInterval        = 60 * time.Second
	StandaloneFirstMax   = 5 * time.Minute
	StandaloneMin        = 25 * time.Minute
	StandaloneMax        = 35 * time.Minute
	MinNextSync          = 15 * time.Second
	MaxNextSync          = 600 * time.Second
	// FreshFollowUps is how many waits after a want_fresh sync are cut to
	// MinNextSync while the shown numbers have not changed yet.
	FreshFollowUps = 6

	defaultBatch = backlog.MaxItems
	minWait      = time.Second
)

// Modes.
const (
	ModeStarting    = "starting"
	ModeCoordinated = "coordinated"
	ModeStandalone  = "standalone"
)

// Logins gives the current sign-in (without the token).
type Logins interface {
	Current(ctx context.Context) creds.Login
}

// Fetcher asks Anthropic for the usage once.
type Fetcher interface {
	Fetch(ctx context.Context) (anthropic.Result, error)
}

// Syncer sends one sync request to one server URL.
type Syncer interface {
	Sync(ctx context.Context, baseURL string, req protocol.Request) (protocol.Response, error)
}

// Identity is what the device reports about itself.
type Identity struct {
	DeviceID, Hostname, OSUser, OS, OSVersion, Arch, Version, Mode string
}

// Deps are the engine's inputs.
type Deps struct {
	Clock      clock.Clock
	Logins     Logins
	Fetcher    Fetcher
	Syncer     Syncer
	Backlog    *backlog.Store
	Config     config.Config
	StatePath  string
	LatestPath string
	Identity   Identity
	Publish    func(status.Snapshot)
	Rand       func() float64
	Log        *log.Logger
}

// Engine holds the loop's state between steps.
type Engine struct {
	d       Deps
	state   config.State
	limiter anthropic.Limiter

	mode           string
	failures       int
	nextStandalone time.Time
	batchLimit     int

	lastResp    *protocol.Response
	serverURL   string
	lastSyncAt  time.Time
	lastSyncOK  bool
	syncProblem string

	lastFetchAt     time.Time
	lastFetchStatus *int
	lastFetchNote   string

	latest      *protocol.Latest
	latestSaved string

	// Safe from any goroutine.
	fresh      atomic.Bool   // a refresh was asked for and not yet sent with a 200
	wake       chan struct{} // capacity 1: cuts Run's wait short
	modeMu     sync.Mutex
	clientMode string

	// Engine goroutine only.
	followUps    int    // short waits left after a want_fresh sync
	freshBase    string // latest fetched_at when the refresh was sent
	freshWaiting bool   // this step's wait is a follow-up wait
}

// New prepares an engine; it reads state.json and latest.json.
func New(d Deps) *Engine {
	if d.Clock == nil {
		d.Clock = clock.Real{}
	}
	if d.Rand == nil {
		d.Rand = rand.Float64
	}
	if d.Log == nil {
		d.Log = log.New(io.Discard, "", 0)
	}
	if d.Publish == nil {
		d.Publish = func(status.Snapshot) {}
	}
	e := &Engine{d: d, mode: ModeStarting, batchLimit: defaultBatch, wake: make(chan struct{}, 1), clientMode: d.Identity.Mode}
	if d.StatePath != "" {
		e.state = config.LoadState(d.StatePath)
	}
	e.limiter.Restore(e.state.FetchLimit)
	if b := e.state.BatchLimit; b >= 1 && b < defaultBatch {
		e.batchLimit = b
	}
	if d.LatestPath != "" {
		if b, err := os.ReadFile(d.LatestPath); err == nil {
			var l protocol.Latest
			if json.Unmarshal(b, &l) == nil && l.FetchedAt != "" {
				e.latest, e.latestSaved = &l, l.FetchedAt
			}
		}
	}
	return e
}

// Run steps until ctx ends, waiting on clk between steps, or less when
// RequestFresh wakes it.
func Run(ctx context.Context, e *Engine, clk clock.Clock) {
	for ctx.Err() == nil {
		d := e.Step(ctx)
		select {
		case <-ctx.Done():
			return
		case <-clk.After(d):
		case <-e.wake:
		}
	}
}

// RequestFresh asks for new numbers: the next sync carries want_fresh once
// (until a sync succeeds) and Run steps at once. In stand-alone mode it only
// makes Run look for the server again; it never fetches directly.
func (e *Engine) RequestFresh() {
	e.fresh.Store(true)
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// SetClientMode sets what the next requests report as client.mode.
func (e *Engine) SetClientMode(mode string) {
	e.modeMu.Lock()
	e.clientMode = mode
	e.modeMu.Unlock()
}

func (e *Engine) currentClientMode() string {
	e.modeMu.Lock()
	defer e.modeMu.Unlock()
	return e.clientMode
}

func (e *Engine) latestAt() string {
	if e.latest == nil {
		return ""
	}
	return e.latest.FetchedAt
}

// Step runs one round and returns how long to wait before the next.
func (e *Engine) Step(ctx context.Context) time.Duration {
	select { // a wake-up from before this step is answered by this step
	case <-e.wake:
	default:
	}
	now := e.d.Clock.Now()
	login := e.d.Logins.Current(ctx)
	before := e.latestAt()
	if err := e.d.Backlog.Prune(now); err != nil {
		e.d.Log.Printf("backlog: %v", err)
	}

	if e.mode == ModeStandalone && !now.Before(e.nextStandalone) && e.canFetch(login, now) {
		e.fetch(ctx, login)
		gap := StandaloneMin + time.Duration(e.d.Rand()*float64(StandaloneMax-StandaloneMin))
		if wait := e.limiter.NextAllowed().Sub(now); wait > gap {
			gap = wait
		}
		e.nextStandalone = now.Add(gap)
	}

	var wait time.Duration
	wantFresh := e.fresh.Swap(false)
	resp, err := e.syncOnce(ctx, login, now, wantFresh)
	if err == nil {
		if wantFresh {
			e.followUps, e.freshBase = FreshFollowUps, before
		}
		wait = clampNextSync(resp.NextSyncSeconds)
		ordered := resp.FetchNow && resp.Role == "poller"
		if ordered && !e.canFetch(login, e.d.Clock.Now()) {
			// Not an error: no result goes back and the server hands over.
			e.noteFetch("fetch order not followed (sign-in or local fetch limit)")
		}
		if ordered && e.canFetch(login, e.d.Clock.Now()) {
			e.fetch(ctx, login)
			if resp2, err2 := e.syncOnce(ctx, login, e.d.Clock.Now(), false); err2 == nil {
				wait = clampNextSync(resp2.NextSyncSeconds)
			} else {
				wait = e.failed(err2, now, login)
			}
		}
		e.freshWaiting = false
		if e.followUps > 0 {
			if e.latestAt() != e.freshBase {
				e.followUps = 0 // the new numbers are here
			} else if e.lastSyncOK {
				wait = min(wait, MinNextSync)
				e.followUps--
				e.freshWaiting = true
			}
		}
	} else {
		wait = e.failed(err, now, login)
		if e.mode == ModeStandalone {
			e.followUps = 0 // no server: nothing will come of the request
		} else if wantFresh {
			e.fresh.CompareAndSwap(false, true) // try again with the next sync
		}
		e.freshWaiting = e.followUps > 0
	}
	e.publish(login, e.d.Clock.Now())
	return wait
}

// canFetch: a usable sign-in with a known account (a result without one is
// dropped by the server) and room under the agent's own fetch limit.
func (e *Engine) canFetch(login creds.Login, now time.Time) bool {
	return login.State(now) == "ok" && login.Account != nil && login.Account.UUID != "" && e.limiter.Allowed(now)
}

func clampNextSync(s int) time.Duration {
	d := time.Duration(s) * time.Second
	if d < MinNextSync {
		return MinNextSync
	}
	if d > MaxNextSync {
		return MaxNextSync
	}
	return d
}

// failed counts a sync failure and returns the wait before the next step.
func (e *Engine) failed(err error, now time.Time, login creds.Login) time.Duration {
	e.failures++
	e.lastSyncOK = false
	e.syncProblem = err.Error()
	if e.failures == 1 || e.failures%10 == 0 {
		e.d.Log.Printf("sync failed (%d in a row): %s", e.failures, e.syncProblem)
	}
	var se *protocol.StatusError
	if errors.As(err, &se) && se.Status == 413 {
		e.batchLimit = max(1, e.batchLimit/2)
		e.state.BatchLimit = e.batchLimit
		e.saveState()
		e.d.Log.Printf("server says the request is too large; sending at most %d results", e.batchLimit)
	}
	if e.failures >= FailuresToStandalone && e.mode != ModeStandalone {
		e.setMode(ModeStandalone)
		e.nextStandalone = now.Add(time.Duration(e.d.Rand() * float64(StandaloneFirstMax)))
	}
	wait := RetryInterval
	if e.mode == ModeStandalone {
		due := e.nextStandalone
		if na := e.limiter.NextAllowed(); na.After(due) {
			due = na
		}
		if login.State(now) == "ok" {
			if d := due.Sub(e.d.Clock.Now()); d < wait {
				wait = max(d, minWait)
			}
		}
	}
	return wait
}

func (e *Engine) setMode(m string) {
	if e.mode != m {
		e.d.Log.Printf("mode: %s -> %s", e.mode, m)
		e.mode = m
	}
}

// syncOnce tries the candidate URLs in order; the first success wins.
func (e *Engine) syncOnce(ctx context.Context, login creds.Login, now time.Time, wantFresh bool) (protocol.Response, error) {
	req := e.request(login, now)
	req.WantFresh = wantFresh
	var lastErr, serverErr error
	for _, base := range discover.Candidates(e.state.LastGoodURL, e.state.ServerURLs, e.d.Config.ServerURLs) {
		if ctx.Err() != nil {
			break
		}
		resp, err := e.d.Syncer.Sync(ctx, base, req)
		if err != nil {
			lastErr = err
			if isServerError(err) && serverErr == nil {
				serverErr = err
			}
			continue
		}
		e.synced(base, resp, req.Results, e.d.Clock.Now())
		return resp, nil
	}
	if serverErr != nil {
		return protocol.Response{}, serverErr
	}
	if lastErr == nil {
		lastErr = errors.New("sync: no server address to try")
		if ctx.Err() != nil {
			lastErr = ctx.Err()
		}
	}
	return protocol.Response{}, lastErr
}

// isServerError: the server (or something posing as it) answered.
func isServerError(err error) bool {
	var se *protocol.StatusError
	return errors.Is(err, protocol.ErrBadIngestKey) || errors.Is(err, protocol.ErrUnsupportedSchema) ||
		errors.Is(err, protocol.ErrBadResponse) || errors.As(err, &se)
}

func (e *Engine) synced(base string, resp protocol.Response, sent []protocol.Result, now time.Time) {
	if err := e.d.Backlog.Remove(sent); err != nil {
		e.d.Log.Printf("backlog: %v", err)
	}
	if e.failures > 0 {
		e.d.Log.Printf("sync ok again via %s after %d failures", base, e.failures)
	}
	e.failures = 0
	e.setMode(ModeCoordinated)
	e.lastSyncOK, e.lastSyncAt, e.syncProblem, e.serverURL = true, now, "", base
	r := resp
	e.lastResp = &r

	changed := false
	if e.batchLimit != defaultBatch {
		e.batchLimit = defaultBatch
		e.state.BatchLimit = 0
		changed = true
	}
	if e.state.LastGoodURL != base {
		e.state.LastGoodURL = base
		changed = true
	}
	announced := normalizeAll(resp.ServerURLs)
	if !equalStrings(announced, e.state.ServerURLs) {
		e.state.ServerURLs = announced
		changed = true
	}
	if changed {
		e.saveState()
	}
	if resp.Latest != nil && resp.Latest.FetchedAt != "" {
		e.setLatest(*resp.Latest)
	}
}

func normalizeAll(in []string) []string {
	var out []string
	for _, u := range in {
		if n, err := config.NormalizeURL(u); err == nil && len(out) < 10 {
			out = append(out, n)
		}
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (e *Engine) saveState() {
	if e.d.StatePath == "" {
		return
	}
	if err := config.SaveState(e.d.StatePath, e.state); err != nil {
		e.d.Log.Printf("state: %v", err)
	}
}

func (e *Engine) setLatest(l protocol.Latest) {
	if e.latest != nil && newer(e.latest.FetchedAt, l.FetchedAt) {
		return // what is shown is newer (e.g. this device's own fetch)
	}
	e.latest = &l
	if e.d.LatestPath == "" || e.latestSaved == l.FetchedAt {
		return
	}
	b, err := json.Marshal(l)
	if err == nil {
		err = atomicfile.Write(e.d.LatestPath, b, 0o600)
	}
	if err != nil {
		e.d.Log.Printf("latest: %v", err)
		return
	}
	e.latestSaved = l.FetchedAt
}

// newer reports whether a is a later timestamp than b.
func newer(a, b string) bool {
	ta, errA := time.Parse(time.RFC3339Nano, a)
	tb, errB := time.Parse(time.RFC3339Nano, b)
	return errA == nil && errB == nil && ta.After(tb)
}

func (e *Engine) fetch(ctx context.Context, login creds.Login) {
	res, err := e.d.Fetcher.Fetch(ctx)
	if err != nil {
		e.noteFetch("fetch not attempted: " + err.Error())
		return
	}
	e.limiter.Record(res)
	e.state.FetchLimit = e.limiter.Snapshot()
	e.saveState()
	if ctx.Err() != nil {
		return // shutting down: the attempt counts locally, but its "no response" is not a result
	}
	st := res.HTTPStatus
	e.lastFetchAt, e.lastFetchStatus = res.FetchedAt, &st
	note := "fetch: HTTP " + strconv.Itoa(st)
	if res.Problem != "" {
		note += " (" + res.Problem + ")"
	}
	e.noteFetch(note)
	r := protocol.Result{AccountUUID: login.Account.UUID, FetchedAt: protocol.Time{Time: res.FetchedAt}, HTTPStatus: st, Usage: res.Usage}
	if err := e.d.Backlog.Add(r); err != nil {
		e.d.Log.Printf("backlog: %v", err)
	}
	// Like the extension: a 200 without a weekly number does not replace good numbers.
	if st == 200 && res.Usage != nil && usage.ParseLimits(res.Usage, usage.ScopedModel).Weekly.Percent != nil {
		e.setLatest(protocol.Latest{FetchedAt: protocol.Time{Time: res.FetchedAt}.UTC().Format("2006-01-02T15:04:05.000Z"), Usage: res.Usage})
	}
}

func (e *Engine) noteFetch(s string) {
	if s != e.lastFetchNote {
		e.d.Log.Print(s)
		e.lastFetchNote = s
	}
}

// Server-side length caps (characters).
const (
	capText  = 256
	capShort = 64
)

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func opt(s string, n int) protocol.Opt { return protocol.Opt(clip(s, n)) }

func (e *Engine) request(login creds.Login, now time.Time) protocol.Request {
	id := e.d.Identity
	req := protocol.Request{
		Schema: protocol.Schema,
		Client: protocol.ClientInfo{Version: clip(id.Version, capShort), OS: clip(id.OS, capShort),
			OSVersion: opt(id.OSVersion, capText), Arch: opt(id.Arch, capShort), Mode: opt(e.currentClientMode(), capShort)},
		Device:  protocol.DeviceInfo{ID: clip(id.DeviceID, capShort), Hostname: opt(id.Hostname, capText), OSUser: opt(id.OSUser, capText)},
		Auth:    protocol.AuthInfo{State: login.State(now)},
		Results: e.d.Backlog.Pending(e.batchLimit),
	}
	if !login.ExpiresAt.IsZero() {
		req.Auth.TokenExpiresAt = &protocol.Time{Time: login.ExpiresAt}
	}
	if a := login.Account; a != nil && a.UUID != "" {
		req.Account = &protocol.AccountInfo{
			UUID: clip(a.UUID, capShort), Email: opt(a.Email, capText), DisplayName: opt(a.DisplayName, capText),
			OrganizationName: opt(a.OrganizationName, capText), OrganizationType: opt(a.OrganizationType, capShort),
			SubscriptionType: opt(login.SubscriptionType, capShort), RateLimitTier: opt(login.RateLimitTier, capShort),
		}
	}
	return req
}

func (e *Engine) publish(login creds.Login, now time.Time) {
	s := status.Snapshot{
		Version: e.d.Identity.Version, PID: os.Getpid(), UpdatedAt: now.UTC(), Mode: e.mode,
		ServerURL: e.serverURL, LastSyncAt: e.lastSyncAt.UTC(), LastSyncOK: e.lastSyncOK,
		SyncFailures: e.failures, SyncProblem: e.syncProblem,
		LastFetchAt: e.lastFetchAt.UTC(), LastFetchStatus: e.lastFetchStatus,
		AuthState: login.State(now), SignInProblem: login.Problem, Backlog: e.d.Backlog.Len(),
		ClientMode: e.currentClientMode(), FreshPending: e.fresh.Load() || e.freshWaiting,
	}
	if e.mode == ModeStandalone {
		s.NextStandaloneFetch = e.nextStandalone.UTC()
	}
	if !login.ExpiresAt.IsZero() {
		s.TokenExpiresAt = login.ExpiresAt.UTC()
	}
	if login.Account != nil {
		s.AccountEmail = login.Account.Email
	}
	if r := e.lastResp; r != nil {
		s.Role = r.Role
		s.PeersDevices = r.Peers.Devices
		if r.Peers.PollerHostname != nil {
			s.PollerHostname = *r.Peers.PollerHostname
		}
		if r.ClientLatestVersion != nil {
			s.ClientLatestVersion = *r.ClientLatestVersion
		}
	}
	if l := e.latest; l != nil {
		if t, err := time.Parse(time.RFC3339Nano, l.FetchedAt); err == nil {
			s.LatestFetchedAt = t.UTC()
		}
		lim := usage.ParseLimits(l.Usage, usage.ScopedModel)
		s.WeeklyPercent, s.ScopedPercent = lim.Weekly.Percent, lim.Scoped.Percent
		if lim.Weekly.ResetsAt != nil {
			s.WeeklyResetsAt = lim.Weekly.ResetsAt.UTC()
		}
		if lim.Weekly.Percent != nil {
			s.WeeklySeverity = lim.Weekly.Severity
		}
		if lim.Scoped.Percent != nil {
			s.ScopedSeverity = lim.Scoped.Severity
		}
		if lim.Scoped.ResetsAt != nil {
			s.ScopedResetsAt = lim.Scoped.ResetsAt.UTC()
		}
	}
	e.d.Publish(s)
}
