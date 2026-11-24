package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/autostart"
	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/paths"
	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/procs"
	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/usage"
	"github.com/Deep-In-Sight/claude-usage-client/agent/internal/version"
)

func statusCmd(env Env, args []string) int {
	asJSON := false
	for _, a := range args {
		if a != "--json" {
			fmt.Fprintf(env.Stderr, "claude-usage status: unknown argument %q\n", a)
			return 2
		}
		asJSON = true
	}
	snap, err := status.Read(env.Dirs.File(paths.StatusFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(env.Stderr, "claude-usage: no status yet (is the agent running?)")
		} else {
			fmt.Fprintln(env.Stderr, "claude-usage: status file:", err)
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
	fmt.Fprintf(w, "Claude Usage agent %s\n", version.Version)
	if method != "" {
		fmt.Fprintf(w, "Service:    %s (%s)\n", service, method)
	} else {
		fmt.Fprintf(w, "Service:    %s\n", service)
	}
	if !running {
		fmt.Fprintln(w, "Process:    not running")
	}
	mode := []string{snap.Mode}
	if snap.Role != "" {
		mode = append(mode, snap.Role)
	}
	if snap.ServerURL != "" {
		mode = append(mode, "server "+snap.ServerURL)
	}
	clientMode := ""
	if snap.ClientMode != "" {
		clientMode = " (" + snap.ClientMode + ")"
	}
	fmt.Fprintf(w, "Mode:       %s%s\n", strings.Join(mode, ", "), clientMode)
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
	if snap.LastFetchStatus != nil {
		fmt.Fprintf(w, "Last fetch: %s HTTP %d\n", clockOf(snap.LastFetchAt), *snap.LastFetchStatus)
	}
	signIn := snap.AuthState
	if !snap.TokenExpiresAt.IsZero() {
		signIn += ", expires " + clockOf(snap.TokenExpiresAt)
	}
	if snap.SignInProblem != "" {
		signIn += " (" + snap.SignInProblem + ")"
	}
	fmt.Fprintf(w, "Sign-in:    %s\n", signIn)
	stale := ""
	if !snap.LatestFetchedAt.IsZero() && usage.Stale(snap.LatestFetchedAt, now) {
		stale = " ⚠"
	}
	var resets *time.Time
	if !snap.WeeklyResetsAt.IsZero() {
		r := snap.WeeklyResetsAt
		resets = &r
	}
	fmt.Fprintf(w, "Weekly:     %s  %s%s\n", usage.FormatPercent(snap.WeeklyPercent), usage.FormatReset(resets, now), stale)
	if snap.ScopedPercent != nil {
		fmt.Fprintf(w, "%-11s %s\n", usage.ScopedModel+":", usage.FormatPercent(snap.ScopedPercent))
	}
	if !snap.LatestFetchedAt.IsZero() {
		fmt.Fprintf(w, "Numbers:    %s\n", usage.FormatUpdated(snap.LatestFetchedAt, now))
	}
	fmt.Fprintf(w, "Backlog:    %d\n", snap.Backlog)
	return 0
}
