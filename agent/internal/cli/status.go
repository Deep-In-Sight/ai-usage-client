package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/autostart"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/machine"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/paths"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/procs"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/text"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/usage"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/version"
)

func statusCmd(env Env, args []string) int {
	asJSON := false
	for _, a := range args {
		if a != "--json" {
			fmt.Fprintf(env.Stderr, "ai-usage status: unknown argument %q\n", a)
			return 2
		}
		asJSON = true
	}
	snap, err := status.Read(env.Dirs.File(paths.StatusFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(env.Stderr, "ai-usage: no status yet (is the agent running?)")
		} else {
			fmt.Fprintln(env.Stderr, "ai-usage: status file:", err)
		}
		return 1
	}
	// The method in use: on Linux either the systemd service or XDG
	// autostart, whichever is registered.
	service, method := "unsupported", ""
	var methods []autostart.Method
	if m, err := env.Autostart(true); err == nil {
		methods = append(methods, m)
	}
	methods = append(methods, env.others(true)...)
	for i, m := range methods {
		st := m.State(background())
		if i == 0 || st != "not installed" && st != "not registered" {
			service, method = st, m.Name()
		}
		if st != "not installed" && st != "not registered" {
			break
		}
	}
	running := false
	if pids, err := procs.FindByExe(env.Dirs.InstallExe); err == nil {
		running = slices.ContainsFunc(pids, func(p int) bool { return p != os.Getpid() })
	}
	if asJSON {
		b, _ := json.MarshalIndent(struct {
			Snapshot status.Snapshot `json:"snapshot"`
			Service  string          `json:"service"`
			Running  bool            `json:"running"`
		}{snap, service, running}, "", "  ")
		fmt.Fprintln(env.Stdout, string(b))
		return 0
	}

	now := env.Clock.Now().Local()
	clockOf := func(t time.Time) string {
		if t.IsZero() {
			return "never"
		}
		return usage.FormatClock(t.In(now.Location()))
	}
	w := env.Stdout
	fmt.Fprintf(w, "AI Usage agent %s\n", version.Version)
	if method != "" {
		fmt.Fprintf(w, "Service:    %s (%s)\n", service, method)
	} else {
		fmt.Fprintf(w, "Service:    %s\n", service)
	}
	if !running {
		fmt.Fprintln(w, "Process:    not running")
	}
	mode := []string{snap.Mode}
	if snap.ServerURL != "" {
		mode = append(mode, "server "+snap.ServerURL)
	}
	clientMode := ""
	if snap.ClientMode != "" {
		clientMode = " (" + snap.ClientMode + ")"
	}
	fmt.Fprintf(w, "Mode:       %s%s\n", strings.Join(mode, ", "), clientMode)
	// whether the running agent identifies the PC, and how; never the key
	// (design D8), and from what the agent wrote, not read here (review m-M)
	fmt.Fprintf(w, "Machine:    %s\n", machineWords(snap.Machine))
	syncState := "ok"
	if !snap.LastSyncOK {
		syncState = "failing"
		if snap.SyncProblem != "" {
			syncState += ": " + snap.SyncProblem
		}
	}
	fmt.Fprintf(w, "Last sync:  %s %s\n", clockOf(snap.LastSyncAt), syncState)
	if snap.SchemaFallback {
		fmt.Fprintln(w, "Protocol:   schema 1, Claude only (the server does not support schema 2 yet)")
	}
	if snap.Mode == "standalone" && !snap.NextStandaloneFetch.IsZero() {
		fmt.Fprintf(w, "Next fetch: %s (stand-alone)\n", clockOf(snap.NextStandaloneFetch))
	}
	for _, id := range []provider.ID{provider.Claude, provider.Codex} {
		if ps, ok := snap.Providers[id]; ok {
			writeProvider(w, env, id, ps, now)
		}
	}
	fmt.Fprintf(w, "Backlog:    %d\n", snap.Backlog)
	fmt.Fprintf(w, "Log:        %s\n", displayPath(env, env.Dirs.File(paths.LogFile)))
	return 0
}

// machineWords is the status text for status.Snapshot.Machine.
func machineWords(m string) string {
	switch m {
	case machine.SourceOS:
		return "identified (OS machine id)"
	case status.MachineNone:
		return "not identified (this OS gives no machine identifier; a reinstall counts as a new PC)"
	case status.MachineNotSent:
		return "not sent (the server speaks the older protocol)"
	case "":
		return "not reported (the running agent is older)"
	}
	return m
}

// signInWords are the status text of the sign-in states.
var signInWords = map[string]string{"ok": "signed in", "expired": "sign-in expired", "not_signed_in": "not signed in",
	"unsupported_store": "sign-in in the system keychain (not supported)", "api_key": "API-key sign-in (no plan limits)",
	"unsupported_base_url": "server address in config.toml not supported"}

// writeProvider prints one provider's part (design 6.12): its sign-in and
// role on the first line, then one line per limit, when the numbers were
// fetched and the last fetch of this PC.
func writeProvider(w io.Writer, env Env, id provider.ID, ps status.ProviderStatus, now time.Time) {
	const indent = "            "
	when := func(t time.Time) string {
		t = t.In(now.Location())
		if y, m, d := t.Date(); y == now.Year() && m == now.Month() && d == now.Day() {
			return usage.FormatClock(t)
		}
		return t.Format("Jan 2 15:04")
	}
	head := signInWords[ps.State]
	if head == "" {
		head = ps.State
	}
	if id == provider.Codex && ps.Plan != "" && (ps.State == "ok" || ps.State == "expired") {
		head += " (" + capitalise(ps.Plan) + ")"
	}
	if !ps.ExpiresAt.IsZero() {
		head += ", expires " + when(ps.ExpiresAt)
	}
	switch {
	case ps.Role == "poller":
		head += " · poller"
	case ps.Role == "standby" && ps.PollerHostname != "":
		head += " · standby (" + ps.PollerHostname + ")"
	case ps.Role != "":
		head += " · " + ps.Role
	}
	if ps.Problem != "" {
		head += " (" + ps.Problem + ")"
	}
	fmt.Fprintf(w, "%-12s%s\n", id.Name()+":", head)
	labelW, pctW := 0, 0
	for _, r := range ps.Rows {
		labelW = max(labelW, utf8.RuneCountInString(r.Label))
		pctW = max(pctW, utf8.RuneCountInString(usage.FormatPercent(r.Percent)))
	}
	for _, r := range ps.Rows {
		pct := usage.FormatPercent(r.Percent)
		line := r.Label + strings.Repeat(" ", labelW-utf8.RuneCountInString(r.Label)+1) +
			strings.Repeat(" ", pctW-utf8.RuneCountInString(pct)) + pct
		if r.Blocked {
			line += " blocked"
		}
		if r.ResetsAt != nil {
			line += "  " + usage.FormatReset(r.ResetsAt, now)
		}
		fmt.Fprintln(w, indent+line)
	}
	if ps.ParseProblem != "" {
		fmt.Fprintln(w, indent+text.ReplyUnreadable)
	}
	var numbers []string
	if !ps.LatestFetchedAt.IsZero() {
		u := usage.FormatUpdated(ps.LatestFetchedAt, now)
		if usage.Stale(ps.LatestFetchedAt, now) {
			u += " ⚠"
		}
		numbers = append(numbers, u)
	}
	if ps.LastFetchStatus != nil {
		numbers = append(numbers, fmt.Sprintf("last fetch %s HTTP %d", when(ps.LastFetchAt), *ps.LastFetchStatus))
	}
	if len(numbers) > 0 {
		fmt.Fprintln(w, indent+strings.Join(numbers, " · "))
	}
	if id == provider.Codex && (ps.LastFetchSource != "" || ps.FallbackCLI != "" || ps.FallbackProblem != "") {
		// how this PC got Codex's numbers, and the Codex CLI the fallback
		// would run (design 6.12)
		src := map[string]string{"wham": "direct", "app-server": "Codex CLI", "": "no fetch from this PC yet"}[ps.LastFetchSource]
		if src == "" {
			src = ps.LastFetchSource
		}
		cli := "no fallback CLI found"
		switch {
		case ps.FallbackProblem != "":
			cli = "fallback CLI: " + ps.FallbackProblem
		case ps.FallbackCLI != "":
			path, ver := ps.FallbackCLI, ""
			if i := strings.LastIndexByte(path, ' '); i > 0 {
				path, ver = path[:i], path[i:]
			}
			cli = "fallback CLI: " + displayPath(env, path) + ver
		}
		fmt.Fprintf(w, "%ssource: %s  (%s)\n", indent, src, cli)
	}
}

// capitalise is a plan name as the menu shows it ("plus" -> "Plus").
func capitalise(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// displayPath shows a path under the home folder as ~/... (not on Windows,
// where ~ means nothing to the shell).
func displayPath(env Env, p string) string {
	if env.goos() == "windows" || env.Dirs.Home == "" {
		return p
	}
	if rel, err := filepath.Rel(env.Dirs.Home, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "~/" + filepath.ToSlash(rel)
	}
	return p
}
