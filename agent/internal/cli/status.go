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
	// whether the PC is identified, and how; never the key (design D8)
	_, source := env.machine()
	fmt.Fprintf(w, "Machine:    %s\n", map[string]string{machine.SourceOS: "identified (OS machine id)",
		machine.SourceMAC: "identified (network card address)", "": "not identified"}[source])
	syncState := "ok"
	if !snap.LastSyncOK {
		syncState = "failing"
		if snap.SyncProblem != "" {
			syncState += ": " + snap.SyncProblem
		}
	}
	fmt.Fprintf(w, "Last sync:  %s %s\n", clockOf(snap.LastSyncAt), syncState)
	if snap.Mode == "standalone" && !snap.NextStandaloneFetch.IsZero() {
		fmt.Fprintf(w, "Next fetch: %s (stand-alone)\n", clockOf(snap.NextStandaloneFetch))
	}
	for _, id := range []provider.ID{provider.Claude, provider.Codex} {
		if ps, ok := snap.Providers[id]; ok {
			writeProvider(w, id, ps, now)
		}
	}
	fmt.Fprintf(w, "Backlog:    %d\n", snap.Backlog)
	fmt.Fprintf(w, "Log:        %s\n", displayPath(env, env.Dirs.File(paths.LogFile)))
	return 0
}

// signInWords are the status text of the sign-in states.
var signInWords = map[string]string{"ok": "signed in", "expired": "sign-in expired", "not_signed_in": "not signed in"}

// writeProvider prints one provider's part (design 6.12): its sign-in and
// role on the first line, then one line per limit, when the numbers were
// fetched and the last fetch of this PC.
func writeProvider(w io.Writer, id provider.ID, ps status.ProviderStatus, now time.Time) {
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
