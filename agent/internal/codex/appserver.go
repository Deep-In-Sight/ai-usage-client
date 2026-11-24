package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/procs"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
)

// SpawnMargin: the fallback runs codex only while the access token has
// more than this left, so that the codex it starts never finds the token
// inside Codex's own 5-minute renewal window and renews it (design D2,
// stage 0 §3).
const SpawnMargin = 30 * time.Minute

// The fallback's time budget (controller ruling R1): one run of codex,
// its requests, the end of the process and its reaping all fit inside
// the caller's deadline (the engine's 35 s cap on a fetch, the direct
// request included).
const (
	// MaxCall bounds a run when the caller gives no deadline.
	MaxCall = 30 * time.Second
	// MinCall: with less time than this left for the requests, codex is
	// not started (one answer takes about 2.5 s online, stage 0 §4).
	MinCall = 8 * time.Second
	// ExitGrace is how long codex may take to end after its input closed
	// (it takes 0.03–5 s, stage 0 §4) before it is killed.
	ExitGrace = 5 * time.Second
	// ReapMargin is kept for ending the process group (SIGTERM, at most
	// termWait, then SIGKILL) and reaping it.
	ReapMargin = time.Second
	// StartGap: at most one codex start per 10 minutes (design D11: the
	// fallback's interval is 600 s; each start costs about 8 connections
	// and writes in the user's Codex folder).
	StartGap = 10 * time.Minute
	// termWait is how long the group has after SIGTERM before SIGKILL.
	termWait = 400 * time.Millisecond

	maxRPCLine    = 1 << 20
	maxDetail     = 200
	versionLimit  = 5 * time.Second
	versionOutput = 4 << 10
)

// Problems of a fallback run (status texts; never file content).
const (
	ProblemCLIUpdate   = "Codex CLI update needed"
	problemStart       = "Codex CLI could not be started"
	problemTimeout     = "Codex CLI did not answer in time"
	problemEnded       = "Codex CLI ended without an answer"
	problemNotObject   = "Codex CLI answer is not a JSON object"
	problemRPCError    = "Codex CLI answered with an error"
	problemInitialised = "Codex CLI did not start its session"
)

// CanSpawn: the access token's expiry is known and more than SpawnMargin
// away (design D2). An unknown expiry never allows a start.
func CanSpawn(a Auth, now time.Time) bool {
	return !a.ExpiresAt.IsZero() && a.ExpiresAt.Sub(now) > SpawnMargin
}

// AppServer asks Codex's own CLI for the rate limits, as the fallback of
// the direct request (design D1): `codex app-server --listen stdio://`,
// three JSON-RPC lines, then the process and everything it started end.
// The agent hands codex no token: codex reads its own sign-in.
type AppServer struct {
	CLIs    *CLIFinder  // the codex executables (nil: none)
	Home    string      // the Codex folder, given to codex as CODEX_HOME
	Clock   clock.Clock // nil: the real clock
	Version string      // our version, in clientInfo
	Environ []string    // the environment codex inherits (nil: this process's)
	// Start starts the process (nil: procs.StartGroup: its own process
	// group or job, no console window).
	Start func(*exec.Cmd) (*procs.Group, error)
	// The budget (zero: the constants above).
	MinCall, ExitGrace, ReapMargin, StartGap time.Duration

	mu        sync.Mutex
	lastStart time.Time
}

func (s *AppServer) now() time.Time {
	if s.Clock == nil {
		return time.Now()
	}
	return s.Clock.Now()
}

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// Ready reports whether Fetch would start codex now for sign-in a: the
// token has more than SpawnMargin left, a codex executable is known and
// the last start is StartGap ago. It runs nothing.
func (s *AppServer) Ready(a Auth) bool {
	if s == nil || s.CLIs == nil {
		return false
	}
	now := s.now()
	if !CanSpawn(a, now) {
		return false
	}
	if _, ok := s.CLIs.Cached(); !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastStart.IsZero() || now.Sub(s.lastStart) >= orDefault(s.StartGap, StartGap)
}

// Status is the codex the fallback would run ("<path> <version>", "" when
// none) and why it cannot, if so (ProblemCLIUpdate). It looks for codex
// executables when that is due (CLIFinder.CLI).
func (s *AppServer) Status() (cli, problem string) {
	if s == nil || s.CLIs == nil {
		return "", ""
	}
	c, ok := s.CLIs.CLI()
	if ok {
		cli = c.Path + " " + c.Version
	}
	return cli, s.CLIs.Problem()
}

// rpcMsg is one JSON-RPC line from codex.
type rpcMsg struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (m rpcMsg) isReply(id int) bool {
	var n float64
	return m.Method == "" && json.Unmarshal(m.ID, &n) == nil && n == float64(id)
}

// Fetch runs codex once and reports its answer as a result with Source
// "app-server" (attempted true), or does nothing (attempted false): the
// token has SpawnMargin or less left (or an unknown expiry), no codex is
// known, the last start is less than StartGap ago, or the caller's
// deadline leaves less than MinCall for the requests.
//
// Codex's answer is reported in the server's terms (design 6.3): a result
// is HTTP 200 with the result as the usage; -32600 "authentication
// required" is 401; an error naming 429 or a rate limit is 429; anything
// else, and no answer in time, is 0. Method not found (-32601) also stops
// using that executable until it changes (ProblemCLIUpdate). Detail holds
// the error code and the first 200 bytes of its message.
//
// The run ends inside the caller's deadline: the requests until the
// deadline less ExitGrace and ReapMargin, then the input is closed, codex
// gets ExitGrace to end, and the whole process group is ended
// (SIGTERM, then SIGKILL; Windows: its job) and reaped. Nothing it
// started is left running and no goroutine of the run outlives Fetch.
//
// Ported from steipete/CodexBar (MIT) Sources/CodexBarCore/UsageFetcher.swift
// CodexRPCClient and Sources/CodexBarCore/Host/Process/RPCChildProcessTeardown.swift
// @ a26ca57: initialize with clientInfo, then initialized, then
// account/rateLimits/read; replies matched by id, notifications and
// lines that are not JSON skipped; bounded waits; teardown closes the
// child's input first, then SIGTERM and SIGKILL across the child's process
// tree. Ours adds the 30-minute token check, one start per 10 minutes,
// the deadline-bound budget and the job object on Windows.
func (s *AppServer) Fetch(ctx context.Context, a Auth) (provider.Result, bool) {
	if s == nil || s.CLIs == nil {
		return provider.Result{}, false
	}
	now := s.now()
	if !CanSpawn(a, now) {
		return provider.Result{}, false
	}
	cli, ok := s.CLIs.Cached()
	if !ok {
		return provider.Result{}, false
	}
	exitGrace, reap := orDefault(s.ExitGrace, ExitGrace), orDefault(s.ReapMargin, ReapMargin)
	deadline := time.Now().Add(MaxCall)
	if d, has := ctx.Deadline(); has && d.Before(deadline) {
		deadline = d
	}
	replyBy := deadline.Add(-exitGrace - reap)
	if time.Until(replyBy) < orDefault(s.MinCall, MinCall) || ctx.Err() != nil {
		return provider.Result{}, false
	}
	s.mu.Lock()
	if !s.lastStart.IsZero() && now.Sub(s.lastStart) < orDefault(s.StartGap, StartGap) {
		s.mu.Unlock()
		return provider.Result{}, false
	}
	s.lastStart = now
	s.mu.Unlock()

	res := provider.Result{Provider: provider.Codex, Source: "app-server", CLIVersion: cli.Version,
		FetchedAt: now.UTC().Truncate(time.Millisecond)}
	r := &appRun{s: s, cli: cli, deadline: deadline, replyBy: replyBy, exitGrace: exitGrace, reap: reap}
	r.run(ctx, &res)
	return res, true
}

// appRun is one run of codex app-server.
type appRun struct {
	s                *AppServer
	cli              CLI
	deadline         time.Time
	replyBy          time.Time
	exitGrace, reap  time.Duration
	group            *procs.Group
	stdin            io.WriteCloser
	lines            chan rpcMsg
	readDone, exited chan struct{}
}

func (r *appRun) environ() []string {
	env := r.s.Environ
	if env == nil {
		env = os.Environ()
	}
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(strings.ToUpper(kv), "CODEX_HOME=") {
			out = append(out, kv)
		}
	}
	return append(out, "CODEX_HOME="+r.s.Home)
}

func (r *appRun) run(ctx context.Context, res *provider.Result) {
	cmd := exec.Command(r.cli.Path, "app-server", "--listen", "stdio://")
	cmd.Env = r.environ()
	cmd.Dir = r.s.Home
	if st, err := os.Stat(r.s.Home); err != nil || !st.IsDir() {
		cmd.Dir = ""
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		res.Problem = problemStart
		return
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		res.Problem = problemStart
		return
	}
	cmd.Stdout = pw // stderr: the null device (codex's own log is in its folder)
	start := r.s.Start
	if start == nil {
		start = procs.StartGroup
	}
	g, err := start(cmd)
	pw.Close()
	if err != nil {
		pr.Close()
		res.Problem = problemStart
		return
	}
	r.group, r.stdin = g, stdin
	r.lines, r.readDone, r.exited = make(chan rpcMsg, 16), make(chan struct{}), make(chan struct{})
	go r.read(pr)
	go func() {
		_ = cmd.Wait()
		close(r.exited)
	}()
	defer r.end(ctx, pr)
	r.talk(ctx, res)
}

// read passes every JSON object line to lines until codex's output ends.
func (r *appRun) read(pr *os.File) {
	defer close(r.readDone)
	defer close(r.lines)
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 64<<10), maxRPCLine)
	for sc.Scan() {
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 || b[0] != '{' {
			continue
		}
		var m rpcMsg
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		if m.Result != nil {
			m.Result = append(json.RawMessage(nil), m.Result...)
		}
		r.lines <- m
	}
}

func (r *appRun) send(line string) bool {
	_, err := io.WriteString(r.stdin, line+"\n")
	return err == nil
}

// await waits for the reply to id until replyBy; notifications and other
// lines are skipped. ok false: no reply (res.Problem says why).
func (r *appRun) await(ctx context.Context, id int, res *provider.Result) (rpcMsg, bool) {
	t := time.NewTimer(time.Until(r.replyBy))
	defer t.Stop()
	for {
		select {
		case m, open := <-r.lines:
			if !open {
				res.Problem = problemEnded
				return rpcMsg{}, false
			}
			if m.isReply(id) {
				return m, true
			}
		case <-t.C:
			res.Problem = problemTimeout
			return rpcMsg{}, false
		case <-ctx.Done():
			res.Problem = problemTimeout
			return rpcMsg{}, false
		}
	}
}

var userAgentVersion = regexp.MustCompile(`^[^/\s]+/(\d+\.\d+\.\d+[0-9A-Za-z.+-]*)`)

func (r *appRun) talk(ctx context.Context, res *provider.Result) {
	version, _ := json.Marshal(r.s.Version)
	if !r.send(`{"id":0,"method":"initialize","params":{"clientInfo":{"name":"ai_usage","title":"AI Usage","version":` + string(version) + `}}}`) {
		res.Problem = problemEnded
		return
	}
	m, ok := r.await(ctx, 0, res)
	if !ok {
		return
	}
	if m.Error != nil {
		r.rpcError(m, res)
		if res.Problem == problemRPCError {
			res.Problem = problemInitialised
		}
		return
	}
	var init struct {
		UserAgent string `json:"userAgent"`
	}
	if json.Unmarshal(m.Result, &init) == nil {
		if v := userAgentVersion.FindStringSubmatch(init.UserAgent); v != nil {
			res.CLIVersion = v[1]
		}
	}
	if !r.send(`{"method":"initialized"}`) ||
		!r.send(`{"id":1,"method":"account/rateLimits/read","params":{"excludeResetCreditDetails":true}}`) {
		res.Problem = problemEnded
		return
	}
	m, ok = r.await(ctx, 1, res)
	if !ok {
		return
	}
	if m.Error != nil {
		r.rpcError(m, res)
		return
	}
	if t := bytes.TrimSpace(m.Result); len(t) > 0 && t[0] == '{' && json.Valid(t) {
		res.HTTPStatus, res.Usage = 200, json.RawMessage(t)
		return
	}
	res.Problem = problemNotObject
}

// rpcError maps codex's JSON-RPC error to the server's terms (design 6.3).
func (r *appRun) rpcError(m rpcMsg, res *provider.Result) {
	code, msg := m.Error.Code, m.Error.Message
	low := strings.ToLower(msg)
	res.Detail = clipDetail(fmt.Sprintf("%d %s", code, cleanDetail(msg)))
	res.Problem = problemRPCError
	switch {
	case code == -32600 && strings.Contains(low, "authentication required"):
		res.HTTPStatus = 401
	case strings.Contains(low, "429") || strings.Contains(low, "rate limit"):
		res.HTTPStatus = 429
	case code == -32601:
		res.Problem = ProblemCLIUpdate
		r.s.CLIs.Disable(r.cli.Path)
	}
}

// end closes codex's input, gives it ExitGrace (within the deadline) to
// end, then ends the whole group and reaps it; it returns once the
// process and the reading goroutine are gone.
func (r *appRun) end(ctx context.Context, pr *os.File) {
	r.stdin.Close()
	graceEnd := time.Now().Add(r.exitGrace)
	if last := r.deadline.Add(-r.reap); graceEnd.After(last) {
		graceEnd = last
	}
	t := time.NewTimer(time.Until(graceEnd))
	select {
	case <-r.exited:
	case <-t.C:
	case <-ctx.Done():
	}
	t.Stop()
	select {
	case <-r.exited:
	default:
		r.group.Terminate()
		tw := time.NewTimer(min(termWait, r.reap/2)) // the rest of the margin: the kill and the reap
		select {
		case <-r.exited:
		case <-tw.C:
		}
		tw.Stop()
	}
	// whatever codex started and left behind ends too; then the reap
	r.group.Kill()
	<-r.exited
	pr.Close()
	for range r.lines {
	}
	<-r.readDone
	r.group.Release()
}

var (
	emailPattern  = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	opaquePattern = regexp.MustCompile(`[A-Za-z0-9_\-]{24,}(\.[A-Za-z0-9_\-]+){0,2}`)
)

// cleanDetail keeps an error message without e-mail addresses or long
// opaque values (ids, tokens) it might quote.
func cleanDetail(s string) string {
	s = emailPattern.ReplaceAllString(s, "<email>")
	return opaquePattern.ReplaceAllString(s, "<id>")
}

func clipDetail(s string) string {
	if len(s) <= maxDetail {
		return s
	}
	s = s[:maxDetail]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// errVersion: the executable did not print a codex-cli version in time.
var errVersion = errors.New("codex: no version")

// RunVersion runs `<path> --version` (5 s, no console window, its process
// group ended after) and returns what it printed.
func RunVersion(path string) (string, error) {
	cmd := exec.Command(path, "--version")
	var out limitedBuffer
	out.max = versionOutput
	cmd.Stdout = &out
	cmd.WaitDelay = time.Second
	g, err := procs.StartGroup(cmd)
	if err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t := time.NewTimer(versionLimit)
	select {
	case err = <-done:
	case <-t.C:
		err = errVersion
		g.Kill()
		<-done
	}
	t.Stop()
	g.Kill()
	g.Release()
	if err != nil {
		return "", err
	}
	return out.String(), nil
}

// limitedBuffer keeps the first max bytes written to it.
type limitedBuffer struct {
	mu  sync.Mutex
	b   bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if room := l.max - l.b.Len(); room > 0 {
		l.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (l *limitedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// CLIFinder keeps the codex executables and their versions (design D15):
// the list is looked at again at most every Rescan, and an executable's
// version is asked (RunVersion) only when it is new or its file changed.
type CLIFinder struct {
	List    func() []string                   // the candidates in order (FindCLI)
	Version func(path string) (string, error) // nil: RunVersion
	Clock   clock.Clock                       // nil: the real clock
	Rescan  time.Duration                     // 0: 10 minutes

	mu      sync.Mutex
	scanned time.Time
	known   map[string]cliEntry
	order   []string
	bad     map[string]stamp // answered "method not found", until the file changes
	best    CLI
	found   bool
}

type cliEntry struct {
	st      stamp
	version string
}

func (f *CLIFinder) now() time.Time {
	if f.Clock == nil {
		return time.Now()
	}
	return f.Clock.Now()
}

// CLI is the codex to run: the highest version among the candidates, not
// counting one disabled by Disable. It looks again when that is due.
func (f *CLIFinder) CLI() (CLI, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	if f.scanned.IsZero() || now.Sub(f.scanned) >= orDefault(f.Rescan, 10*time.Minute) {
		f.scan(now)
	}
	return f.best, f.found
}

// Cached is CLI without looking again (it never runs anything).
func (f *CLIFinder) Cached() (CLI, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.best, f.found
}

func (f *CLIFinder) scan(now time.Time) {
	f.scanned = now
	if f.known == nil {
		f.known, f.bad = map[string]cliEntry{}, map[string]stamp{}
	}
	version := f.Version
	if version == nil {
		version = RunVersion
	}
	var paths []string
	if f.List != nil {
		paths = f.List()
	}
	f.order = f.order[:0]
	for _, p := range paths {
		st := stampOf(p)
		if !st.exists {
			continue
		}
		f.order = append(f.order, p)
		if e, ok := f.known[p]; ok && e.st == st {
			continue
		}
		v := ""
		if out, err := version(p); err == nil {
			v, _ = ParseVersion(out)
		}
		f.known[p] = cliEntry{st: st, version: v}
	}
	f.pick()
}

// pick chooses among the known candidates (f.mu held).
func (f *CLIFinder) pick() {
	var usable []string
	for _, p := range f.order {
		if st, bad := f.bad[p]; bad {
			if st == f.known[p].st {
				continue
			}
			delete(f.bad, p) // it changed: an update, try it again
		}
		if f.known[p].version != "" {
			usable = append(usable, p)
		}
	}
	f.best, f.found = PickCLI(usable, func(p string) (string, error) { return "codex-cli " + f.known[p].version, nil })
}

// Disable stops using path until its file changes (codex answered
// "method not found": too old for the rate-limit read, O8).
func (f *CLIFinder) Disable(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.known == nil {
		f.known, f.bad = map[string]cliEntry{}, map[string]stamp{}
	}
	f.bad[path] = f.known[path].st
	f.pick()
}

// Problem is ProblemCLIUpdate while no codex is usable because the ones
// found are too old, else "".
func (f *CLIFinder) Problem() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.found && len(f.bad) > 0 {
		return ProblemCLIUpdate
	}
	return ""
}
