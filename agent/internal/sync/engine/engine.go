// Package engine is the agent's loop: sync with the server, fetch when told
// to and sync again at once, and fall back to stand-alone fetching after
// three failed syncs. The coordination rules live on the server; Step is a
// deterministic function of its inputs and the (fake or real) clock.
//
// The engine has one lane per provider (Claude, Codex): each lane has its
// own sign-in, role, fetch limit, stand-alone schedule and newest numbers,
// so one provider's 401 or 429 never delays another's fetch. All lanes
// share one sync per round. The engine sees sign-ins only as
// provider.Login (no token); fetching goes through the provider, which is
// the only path to the token.
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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/anthropic"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/atomicfile"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/config"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/backlog"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/discover"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/protocol"
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
	Clock     clock.Clock
	Providers []provider.Provider // one lane each, in this order
	Syncer    Syncer
	Backlog   *backlog.Store
	Config    config.Config
	StatePath string
	// LatestPath is the file with a provider's newest numbers (nil: none).
	LatestPath func(provider.ID) string
	// LegacyLatestPath is the v0.7/v0.8 latest.json: at the first start it
	// becomes Claude's file.
	LegacyLatestPath string
	Identity         Identity
	Publish          func(status.Snapshot)
	Rand             func() float64
	Log              *log.Logger
}

// lane is one provider's part of the engine. Engine goroutine only.
type lane struct {
	p  provider.Provider
	id provider.ID

	login provider.Login // read at the start of each step

	// from the server's last answer for this provider
	role          string
	fetchNow      bool
	pcs           int
	poller        string
	serverProblem string

	latest      *protocol.Latest
	latestSaved string
	latestPath  string

	limiter        anthropic.Limiter // this provider's own fetch limit
	nextStandalone time.Time

	lastFetchAt     time.Time
	lastFetchStatus *int
	lastFetchNote   string
}

// reply is one provider's part of a server answer.
type reply struct {
	role     string
	fetchNow bool
	latest   *protocol.Latest
	pcs      int
	poller   *string
	problem  string
}

// answer is a server answer in the engine's terms.
type answer struct {
	lanes        map[provider.ID]reply
	next         int
	serverURLs   []string
	clientLatest *string
}

// Engine holds the loop's state between steps.
type Engine struct {
	d     Deps
	state config.State
	lanes []*lane

	mode       string
	failures   int
	batchLimit int

	serverURL    string
	lastSyncAt   time.Time
	lastSyncOK   bool
	syncProblem  string
	clientLatest string

	// Safe from any goroutine.
	fresh      atomic.Bool   // a refresh was asked for and not yet sent with a 200
	wake       chan struct{} // capacity 1: cuts Run's wait short
	modeMu     sync.Mutex
	clientMode string

	// Engine goroutine only.
	followUps    int    // short waits left after a want_fresh sync
	freshBase    string // the lanes' latest fetched_at when the refresh was sent
	freshWaiting bool   // this step's wait is a follow-up wait
}

// New prepares an engine; it reads state.json and each provider's latest
// file (a v0.8 latest.json becomes Claude's first).
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
	if b := e.state.BatchLimit; b >= 1 && b < defaultBatch {
		e.batchLimit = b
	}
	if d.LatestPath != nil && d.LegacyLatestPath != "" {
		migrateLatest(d.LegacyLatestPath, d.LatestPath(provider.Claude), d.Log)
	}
	for _, p := range d.Providers {
		l := &lane{p: p, id: p.ID()}
		l.limiter.Restore(e.savedLimit(l.id))
		if d.LatestPath != nil {
			l.latestPath = d.LatestPath(l.id)
			if b, err := os.ReadFile(l.latestPath); err == nil {
				var x protocol.Latest
				if json.Unmarshal(b, &x) == nil && x.FetchedAt != "" {
					l.latest, l.latestSaved = &x, x.FetchedAt
				}
			}
		}
		e.lanes = append(e.lanes, l)
	}
	return e
}

// migrateLatest renames a v0.7/v0.8 latest.json to Claude's file, unless
// that exists already.
func migrateLatest(legacy, claude string, lg *log.Logger) {
	if _, err := os.Stat(claude); err == nil {
		return
	}
	if _, err := os.Stat(legacy); err != nil {
		return
	}
	if err := os.Rename(legacy, claude); err != nil {
		lg.Printf("latest: %v", err)
	}
}

func (e *Engine) savedLimit(id provider.ID) config.FetchLimit {
	if id == provider.Claude {
		return e.state.FetchLimit
	}
	return e.state.FetchLimits[string(id)]
}

func (e *Engine) saveLimit(l *lane) {
	if l.id == provider.Claude {
		e.state.FetchLimit = l.limiter.Snapshot()
	} else {
		if e.state.FetchLimits == nil {
			e.state.FetchLimits = map[string]config.FetchLimit{}
		}
		e.state.FetchLimits[string(l.id)] = l.limiter.Snapshot()
	}
	e.saveState()
}

// lane returns the lane of provider id, or nil.
func (e *Engine) lane(id provider.ID) *lane {
	for _, l := range e.lanes {
		if l.id == id {
			return l
		}
	}
	return nil
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

// SetClientMode sets what the next requests report as client.mode; a change
// wakes Run so the server hears it soon.
func (e *Engine) SetClientMode(mode string) {
	e.modeMu.Lock()
	changed := e.clientMode != mode
	e.clientMode = mode
	e.modeMu.Unlock()
	if changed {
		select {
		case e.wake <- struct{}{}:
		default:
		}
	}
}

func (e *Engine) currentClientMode() string {
	e.modeMu.Lock()
	defer e.modeMu.Unlock()
	return e.clientMode
}

// latestKey changes whenever any lane's newest numbers change.
func (e *Engine) latestKey() string {
	var b strings.Builder
	for _, l := range e.lanes {
		if l.latest != nil {
			b.WriteString(l.latest.FetchedAt)
		}
		b.WriteByte('|')
	}
	return b.String()
}

// Step runs one round and returns how long to wait before the next.
func (e *Engine) Step(ctx context.Context) time.Duration {
	select { // a wake-up from before this step is answered by this step
	case <-e.wake:
	default:
	}
	now := e.d.Clock.Now()
	for _, l := range e.lanes {
		l.login = l.p.Login(ctx)
	}
	before := e.latestKey()
	if err := e.d.Backlog.Prune(now); err != nil {
		e.d.Log.Printf("backlog: %v", err)
	}

	if e.mode == ModeStandalone {
		for _, l := range e.lanes {
			if now.Before(l.nextStandalone) || !e.canFetch(l, now) {
				continue
			}
			e.fetch(ctx, l)
			gap := StandaloneMin + time.Duration(e.d.Rand()*float64(StandaloneMax-StandaloneMin))
			if wait := l.limiter.NextAllowed().Sub(now); wait > gap {
				gap = wait
			}
			l.nextStandalone = now.Add(gap)
		}
	}

	var wait time.Duration
	wantFresh := e.fresh.Swap(false)
	ans, err := e.syncOnce(ctx, e.d.Clock.Now(), wantFresh)
	if err == nil {
		if wantFresh {
			e.followUps, e.freshBase = FreshFollowUps, before
		}
		wait = clampNextSync(ans.next)
		fetched := false
		for _, l := range e.lanes {
			r, ok := ans.lanes[l.id]
			if !ok || !r.fetchNow || r.role != "poller" {
				continue
			}
			if !e.canFetch(l, e.d.Clock.Now()) {
				// Not an error: no result goes back and the server hands over.
				e.noteFetch(l, "fetch order not followed (sign-in or local fetch limit)")
				continue
			}
			e.fetch(ctx, l)
			fetched = true
		}
		if fetched {
			if ans2, err2 := e.syncOnce(ctx, e.d.Clock.Now(), false); err2 == nil {
				wait = clampNextSync(ans2.next)
			} else {
				wait = e.failed(err2, now)
			}
		}
		e.freshWaiting = false
		if e.followUps > 0 {
			if e.latestKey() != e.freshBase {
				e.followUps = 0 // the new numbers are here
			} else if e.lastSyncOK {
				wait = min(wait, MinNextSync)
				e.followUps--
				e.freshWaiting = true
			}
		}
	} else {
		wait = e.failed(err, now)
		if e.mode == ModeStandalone {
			e.followUps = 0 // no server: nothing will come of the request
		} else if wantFresh {
			e.fresh.CompareAndSwap(false, true) // try again with the next sync
		}
		e.freshWaiting = e.followUps > 0
	}
	e.publish(e.d.Clock.Now())
	return wait
}

// canFetch: a usable sign-in with a known account (a result without one is
// dropped by the server) and room under the lane's own fetch limit.
func (e *Engine) canFetch(l *lane, now time.Time) bool {
	return l.login.State == "ok" && l.login.AccountKey != "" && l.limiter.Allowed(now)
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
func (e *Engine) failed(err error, now time.Time) time.Duration {
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
		for _, l := range e.lanes {
			l.nextStandalone = now.Add(time.Duration(e.d.Rand() * float64(StandaloneFirstMax)))
		}
	}
	wait := RetryInterval
	if e.mode == ModeStandalone {
		for _, l := range e.lanes {
			if l.login.State != "ok" {
				continue
			}
			due := l.nextStandalone
			if na := l.limiter.NextAllowed(); na.After(due) {
				due = na
			}
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
func (e *Engine) syncOnce(ctx context.Context, now time.Time, wantFresh bool) (answer, error) {
	req, sent := e.requestV1(now)
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
		ans := answerV1(resp)
		e.synced(base, ans, sent, e.d.Clock.Now())
		return ans, nil
	}
	if serverErr != nil {
		return answer{}, serverErr
	}
	if lastErr == nil {
		lastErr = errors.New("sync: no server address to try")
		if ctx.Err() != nil {
			lastErr = ctx.Err()
		}
	}
	return answer{}, lastErr
}

// answerV1 is a schema 1 answer: it is about Claude only.
func answerV1(resp protocol.Response) answer {
	return answer{
		lanes: map[provider.ID]reply{provider.Claude: {role: resp.Role, fetchNow: resp.FetchNow, latest: resp.Latest,
			pcs: resp.Peers.Devices, poller: resp.Peers.PollerHostname}},
		next: resp.NextSyncSeconds, serverURLs: resp.ServerURLs, clientLatest: resp.ClientLatestVersion,
	}
}

// isServerError: the server (or something posing as it) answered.
func isServerError(err error) bool {
	var se *protocol.StatusError
	return errors.Is(err, protocol.ErrBadIngestKey) || errors.Is(err, protocol.ErrUnsupportedSchema) ||
		errors.Is(err, protocol.ErrBadResponse) || errors.As(err, &se)
}

func (e *Engine) synced(base string, ans answer, sent []backlog.Item, now time.Time) {
	if err := e.d.Backlog.Remove(sent); err != nil {
		e.d.Log.Printf("backlog: %v", err)
	}
	if e.failures > 0 {
		e.d.Log.Printf("sync ok again via %s after %d failures", base, e.failures)
	}
	e.failures = 0
	e.setMode(ModeCoordinated)
	e.lastSyncOK, e.lastSyncAt, e.syncProblem, e.serverURL = true, now, "", base
	e.clientLatest = ""
	if ans.clientLatest != nil {
		e.clientLatest = *ans.clientLatest
	}
	for _, l := range e.lanes {
		r, ok := ans.lanes[l.id]
		if !ok {
			// not part of this answer (a schema 1 server knows Claude only)
			l.role, l.fetchNow, l.pcs, l.poller, l.serverProblem = "", false, 0, "", ""
			continue
		}
		l.role, l.fetchNow, l.pcs, l.serverProblem = r.role, r.fetchNow, r.pcs, r.problem
		l.poller = ""
		if r.poller != nil {
			l.poller = *r.poller
		}
		if r.latest != nil && r.latest.FetchedAt != "" {
			e.setLatest(l, *r.latest)
		}
	}

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
	announced := normalizeAll(ans.serverURLs)
	if !equalStrings(announced, e.state.ServerURLs) {
		e.state.ServerURLs = announced
		changed = true
	}
	if changed {
		e.saveState()
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

func (e *Engine) setLatest(l *lane, x protocol.Latest) {
	if l.latest != nil && newer(l.latest.FetchedAt, x.FetchedAt) {
		return // what is shown is newer (e.g. this device's own fetch)
	}
	l.latest = &x
	if l.latestPath == "" || l.latestSaved == x.FetchedAt {
		return
	}
	b, err := json.Marshal(x)
	if err == nil {
		err = atomicfile.Write(l.latestPath, b, 0o600)
	}
	if err != nil {
		e.d.Log.Printf("latest: %v", err)
		return
	}
	l.latestSaved = x.FetchedAt
}

// newer reports whether a is a later timestamp than b.
func newer(a, b string) bool {
	ta, errA := time.Parse(time.RFC3339Nano, a)
	tb, errB := time.Parse(time.RFC3339Nano, b)
	return errA == nil && errB == nil && ta.After(tb)
}

func (e *Engine) fetch(ctx context.Context, l *lane) {
	res, err := l.p.Fetch(ctx)
	if err != nil {
		e.noteFetch(l, "fetch not attempted: "+err.Error())
		return
	}
	l.limiter.Record(anthropic.Result{FetchedAt: res.FetchedAt, HTTPStatus: res.HTTPStatus, RetryAfter: res.RetryAfter})
	e.saveLimit(l)
	if ctx.Err() != nil {
		return // shutting down: the attempt counts locally, but its "no response" is not a result
	}
	st := res.HTTPStatus
	l.lastFetchAt, l.lastFetchStatus = res.FetchedAt, &st
	note := "fetch: HTTP " + strconv.Itoa(st)
	if res.Problem != "" {
		note += " (" + res.Problem + ")"
	}
	e.noteFetch(l, note)
	account := res.AccountKey
	if account == "" {
		account = l.login.AccountKey
	}
	item := backlog.Item{Provider: string(l.id), AccountKey: account, FetchedAt: protocol.Time{Time: res.FetchedAt}, HTTPStatus: st,
		Usage: res.Usage, Source: res.Source, CLIVersion: res.CLIVersion, Detail: res.Detail}
	if st == 429 && res.RetryAfter >= time.Second {
		secs := int(res.RetryAfter / time.Second)
		item.RetryAfterSeconds = &secs
	}
	if err := e.d.Backlog.Add(item); err != nil {
		e.d.Log.Printf("backlog: %v", err)
	}
	// Like the extension: a 200 without the main number does not replace good numbers.
	if st == 200 && res.Usage != nil {
		parsed := l.p.Parse(res.Usage)
		if r, ok := parsed.Row(parsed.MainKey); ok && r.Percent != nil {
			e.setLatest(l, protocol.Latest{FetchedAt: protocol.Time{Time: res.FetchedAt}.UTC().Format("2006-01-02T15:04:05.000Z"),
				Source: res.Source, Usage: res.Usage})
		}
	}
}

// noteFetch logs a lane's fetch outcome when it changed. Claude's lines
// keep their v0.8 wording; other providers' carry the provider's name.
func (e *Engine) noteFetch(l *lane, s string) {
	if s == l.lastFetchNote {
		return
	}
	l.lastFetchNote = s
	if l.id != provider.Claude {
		s = string(l.id) + " " + s
	}
	e.d.Log.Print(s)
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

// requestV1 is the schema 1 request, built from the Claude lane as v0.8
// built it, and the backlog items it carries.
func (e *Engine) requestV1(now time.Time) (protocol.Request, []backlog.Item) {
	id := e.d.Identity
	var login provider.Login
	if l := e.lane(provider.Claude); l != nil {
		login = l.login
	}
	state := login.State
	if state == "" {
		state = "not_signed_in"
	}
	items := e.d.Backlog.Pending(e.batchLimit, string(provider.Claude))
	req := protocol.Request{
		Schema: protocol.Schema,
		Client: protocol.ClientInfo{Version: clip(id.Version, capShort), OS: clip(id.OS, capShort),
			OSVersion: opt(id.OSVersion, capText), Arch: opt(id.Arch, capShort), Mode: opt(e.currentClientMode(), capShort)},
		Device: protocol.DeviceInfo{ID: clip(id.DeviceID, capShort), Hostname: opt(id.Hostname, capText), OSUser: opt(id.OSUser, capText)},
		Auth:   protocol.AuthInfo{State: state},
	}
	for _, it := range items {
		req.Results = append(req.Results, it.V1())
	}
	if !login.ExpiresAt.IsZero() {
		req.Auth.TokenExpiresAt = &protocol.Time{Time: login.ExpiresAt}
	}
	if login.AccountKey != "" {
		req.Account = &protocol.AccountInfo{
			UUID: clip(login.AccountKey, capShort), Email: opt(login.Email, capText), DisplayName: opt(login.DisplayName, capText),
			OrganizationName: opt(login.OrganizationName, capText), OrganizationType: opt(login.OrganizationType, capShort),
			SubscriptionType: opt(login.Plan, capShort), RateLimitTier: opt(login.RateLimitTier, capShort),
		}
	}
	return req, items
}

func (e *Engine) publish(now time.Time) {
	s := status.Snapshot{
		Version: e.d.Identity.Version, PID: os.Getpid(), UpdatedAt: now.UTC(), Mode: e.mode,
		ServerURL: e.serverURL, LastSyncAt: e.lastSyncAt.UTC(), LastSyncOK: e.lastSyncOK,
		SyncFailures: e.failures, SyncProblem: e.syncProblem, Backlog: e.d.Backlog.Len(),
		ClientMode: e.currentClientMode(), FreshPending: e.fresh.Load() || e.freshWaiting,
		ClientLatestVersion: e.clientLatest, Providers: map[provider.ID]status.ProviderStatus{},
	}
	for _, l := range e.lanes {
		if e.mode == ModeStandalone && (s.NextStandaloneFetch.IsZero() || l.nextStandalone.Before(s.NextStandaloneFetch)) {
			s.NextStandaloneFetch = l.nextStandalone.UTC()
		}
		ps := status.ProviderStatus{
			State: l.login.State, Problem: l.login.Problem, AccountEmail: l.login.Email, Plan: l.login.Plan,
			Role: l.role, PollerHostname: l.poller, PeersPCs: l.pcs, ServerProblem: l.serverProblem,
			LastFetchAt: l.lastFetchAt.UTC(), LastFetchStatus: l.lastFetchStatus, FreshPending: s.FreshPending,
		}
		if !l.login.ExpiresAt.IsZero() {
			ps.ExpiresAt = l.login.ExpiresAt.UTC()
		}
		var raw json.RawMessage
		if x := l.latest; x != nil {
			if t, err := time.Parse(time.RFC3339Nano, x.FetchedAt); err == nil {
				ps.LatestFetchedAt = t.UTC()
			}
			ps.LatestSource, raw = x.Source, x.Usage
		}
		parsed := l.p.Parse(raw)
		ps.Rows, ps.MainKey = parsed.Rows, parsed.MainKey
		if raw != nil {
			ps.ParseProblem = parsed.ParseProblem
		}
		s.Providers[l.id] = ps
	}
	e.d.Publish(s)
}
