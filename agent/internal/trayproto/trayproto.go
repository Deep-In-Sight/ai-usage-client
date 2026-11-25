// Package trayproto is what the running agent and its tray helpers say to
// each other over the helper's standard input and output: one JSON object
// per line, both ways (design 6.4). The main process sends what to show;
// a helper sends back whether its icon is up and what was clicked. Nothing
// else crosses: no sign-in, no token, no file names beyond the helper's
// own icon folder. What a helper is for (its provider, icon id and
// folder) comes in its first line, not on its command line, which other
// users of the PC can read.
package trayproto

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Version is the protocol version; every line carries it.
const Version = 1

// A helper's exit codes.
const (
	ExitOK           = 0  // quit asked for, or the main process went away (standard input ended)
	ExitRefused      = 2  // started without the right first line (not by the agent)
	ExitSessionLost  = 75 // the desktop session's connection went away (Linux: the session bus)
	ExitSessionEnded = 76 // the session is ending (Windows: WM_ENDSESSION)
	ExitDesktopBack  = 77 // standby: the desktop's tray came (back): try the icon again
	ExitUnsupported  = 78 // the GNOME bridge: no supported GNOME Shell on this session bus (plan PR-10)
)

// EnvToken is the environment variable that hands a helper the one-time
// value its first line must repeat (keeps people from starting a helper
// by hand by mistake; not a security boundary).
const EnvToken = "AI_USAGE_HELPER_TOKEN"

// MaxLine is the longest line, newline included, either side accepts.
const MaxLine = 64 << 10

// Message types. Main to helper: hello (first line), view, notice, quit.
// Helper to main: ready (the icon is up), waiting (it is not, and why),
// click, shell (the GNOME bridge: the panel extension attached or not).
const (
	TypeHello   = "hello"
	TypeView    = "view"
	TypeNotice  = "notice"
	TypeQuit    = "quit"
	TypeReady   = "ready"
	TypeWaiting = "waiting"
	TypeClick   = "click"
	TypeShell   = "shell"
)

// RoleShell is a hello's role for the GNOME bridge, the helper that serves
// the usage panel to the GNOME Shell extension (design 6.4); "" is an icon
// helper.
const RoleShell = "shell"

// Msg is one line.
type Msg struct {
	V     int    `json:"v"`
	Type  string `json:"type"`
	Token string `json:"token,omitempty"` // hello
	// hello: the helper's provider ("" for the neutral one), its Windows
	// notification icon id and the folder for its Windows icon files
	Provider string `json:"provider,omitempty"`
	UID      uint32 `json:"uid,omitempty"`
	IconDir  string `json:"icon_dir,omitempty"`
	// hello: watch the desktop only, show nothing (the helper is stopped
	// after repeated failures); end with ExitDesktopBack when it came back
	Standby bool `json:"standby,omitempty"`
	// hello: Windows icons that helpers which crashed or were killed left
	// behind; the helper removes them before it shows anything
	Stale []StaleIcon `json:"stale,omitempty"`
	// ready (Windows): the window that owns the icon, so that a successor
	// can remove the icon should this helper end without doing so
	Window  uint64 `json:"window,omitempty"`
	Icon    *Icon  `json:"icon,omitempty"`    // view: the tray icon
	Title   string `json:"title,omitempty"`   // view: the text next to the icon (Linux label, macOS title)
	Tooltip string `json:"tooltip,omitempty"` // view
	Items   []Item `json:"items,omitempty"`   // view: the menu, every item of ItemIDs in order
	Text    string `json:"text,omitempty"`    // notice
	ID      string `json:"id,omitempty"`      // click: the item's id
	// waiting: "no StatusNotifierWatcher", "no taskbar", …; shell: "detach",
	// "ping", "owner", "unsupported 44.2", "no shell"
	Why string `json:"why,omitempty"`
	// Fields added after v0.9.0 (design 6.5); every one is left out when
	// not set, so older lines read the same.
	Role       string `json:"role,omitempty"`        // hello: "" an icon helper | RoleShell
	EnableOnce bool   `json:"enable_once,omitempty"` // hello (shell): turn the panel extension on once
	Panel      *Panel `json:"panel,omitempty"`       // view: the usage panel, the same for every helper
	Attached   *bool  `json:"attached,omitempty"`    // shell: the panel extension attached (true) or went away (false)
	Supported  *bool  `json:"supported,omitempty"`   // shell: false when the GNOME Shell is not one the panel supports
	Enabled    bool   `json:"enabled,omitempty"`     // shell: turning the extension on succeeded
}

// Panel is the usage panel (design 6.1): the finished view the Windows and
// macOS helpers draw and the GNOME extension reads (GetView, design 6.4).
// Every text is finished here; a helper or the extension only draws it.
// Every field is always sent; the JSON names never change (Global
// Constraint 4: fields are only added).
type Panel struct {
	Serial     uint64         `json:"serial"`      // the main process's count of panels: +1 on every change
	NewVersion string         `json:"new_version"` // "New version available: 0.9.2"; "": no line
	Sections   []PanelSection `json:"sections"`    // never null; in the order Claude, Codex
	Prompt     string         `json:"prompt"`      // nobody signed in: "Sign in to …" ("" otherwise)
	Server     string         `json:"server"`      // the server line without "Updated …" (plan PR-8)
	Actions    []PanelAction  `json:"actions"`     // dashboard, refresh, autostart, remove, quit (ids of ItemIDs)
	Top        []PanelTop     `json:"top"`         // the numbers in the top bar (plan PR-5)
}

// PanelSection is one provider's group: a header, its problem line and its
// limit rows.
type PanelSection struct {
	Provider string     `json:"provider"` // "claude" | "codex"
	Name     string     `json:"name"`     // "Claude"
	Account  string     `json:"account"`  // "team-a@… · 3 PCs" (Codex: " · Plus")
	Updated  string     `json:"updated"`  // "Updated 18:52", never the stale mark; "" without numbers
	Problem  string     `json:"problem"`  // the problem line ("" for none)
	Stale    bool       `json:"stale"`    // the numbers are old: the group is drawn faded
	Rows     []PanelRow `json:"rows"`     // never null
}

// PanelRow is one limit (or the "More limits" pointer); a click sends its
// ID, the menu's item id ("r.claude.0").
type PanelRow struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`        // "5-hour" | "More limits on the dashboard"
	Percent     *float64 `json:"percent"`      // the bar's length: the shown percent, rounded (4.5 -> 5); null: no value
	PercentText string   `json:"percent_text"` // "42%", "–%"
	Detail      string   `json:"detail"`       // "Resets Fri 21:20 · in 2h 20m", "Blocked · resets …", ""
	Level       string   `json:"level"`        // usage.Level: none | normal | warning | critical
	More        bool     `json:"more"`         // the "More limits on the dashboard" row: no bar
}

// PanelAction is one of the action items, as the menu's item of that id.
type PanelAction struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Enabled  bool   `json:"enabled"`
	Visible  bool   `json:"visible"`
	Checkbox bool   `json:"checkbox"`
	Checked  bool   `json:"checked"`
}

// PanelTop is one number in the top bar: a provider in use (its icon's
// title, level and raw percent), or the neutral "–%" when none is.
type PanelTop struct {
	Provider string   `json:"provider"` // "" for the neutral one
	Text     string   `json:"text"`     // "42%", "42% ⚠", "–%"
	Level    string   `json:"level"`
	Percent  *float64 `json:"percent"` // as reported, not rounded (the icon's)
	Stale    bool     `json:"stale"`
}

// StaleIcon names a Windows notification icon: its window and id.
type StaleIcon struct {
	Window uint64 `json:"window"`
	UID    uint32 `json:"uid"`
}

// Icon is what the tray icon shows; the helper draws it (icon.TrayImage).
type Icon struct {
	Provider string   `json:"provider"` // "claude" | "codex" | "" (the neutral ring)
	Percent  *float64 `json:"percent"`
	Level    string   `json:"level"` // usage.Level
	Stale    bool     `json:"stale"`
}

// Item is one menu item. Every field is always sent.
type Item struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Image    string `json:"image"` // "" | a provider: its 16 px mascot, drawn by the helper
	Enabled  bool   `json:"enabled"`
	Visible  bool   `json:"visible"`
	Checkbox bool   `json:"checkbox"`
	Checked  bool   `json:"checked"`
}

// ItemIDs are the menu's items in menu order: a section per provider
// (header "h.", up to five rows "r.", a problem line "p."), then the
// server line and the actions. A separator follows each section that is
// shown and the server line.
var ItemIDs = []string{"newversion",
	"h.claude", "r.claude.0", "r.claude.1", "r.claude.2", "r.claude.3", "r.claude.4", "p.claude",
	"h.codex", "r.codex.0", "r.codex.1", "r.codex.2", "r.codex.3", "r.codex.4", "p.codex",
	"server", "dashboard", "refresh", "autostart", "remove", "quit"}

// RowsPerSection is how many "r." items a section has.
const RowsPerSection = 5

// Section is the provider whose section id belongs to ("" for the items
// outside the sections).
func Section(id string) string {
	kind, rest, ok := strings.Cut(id, ".")
	if !ok || (kind != "h" && kind != "r" && kind != "p") {
		return ""
	}
	p, _, _ := strings.Cut(rest, ".")
	return p
}

// Sections are the providers that have a section, in menu order.
func Sections() []string {
	var out []string
	for _, id := range ItemIDs {
		if s := Section(id); s != "" && (len(out) == 0 || out[len(out)-1] != s) {
			out = append(out, s)
		}
	}
	return out
}

// ErrTooLong: a line over MaxLine.
var ErrTooLong = fmt.Errorf("trayproto: line longer than %d bytes", MaxLine)

// Encode writes m as one line. A message that would not fit MaxLine is
// refused and nothing is written.
func Encode(w io.Writer, m Msg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(b)+1 > MaxLine {
		return ErrTooLong
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// Decode reads one line. io.EOF means the other side closed its end
// cleanly between lines; ErrTooLong leaves the stream unusable.
func Decode(r *bufio.Reader) (Msg, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > MaxLine {
			return Msg{}, ErrTooLong
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil && !(errors.Is(err, io.EOF) && len(line) > 0) {
			return Msg{}, err
		}
		break
	}
	line = bytes.TrimRight(line, "\r\n")
	var m Msg
	if err := json.Unmarshal(line, &m); err != nil {
		return Msg{}, fmt.Errorf("trayproto: %w", err)
	}
	if m.Type == "" {
		return Msg{}, errors.New("trayproto: a line without a type")
	}
	return m, nil
}
