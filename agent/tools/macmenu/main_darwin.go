//go:build darwin && cgo

package main

/*
#cgo LDFLAGS: -framework ApplicationServices -framework CoreFoundation
#include <ApplicationServices/ApplicationServices.h>
#include <stdio.h>
#include <string.h>
#include <time.h>

// seconds since boot, the clock of NSProcessInfo's systemUptime
static double mm_uptime(void) { return (double)clock_gettime_nsec_np(CLOCK_UPTIME_RAW) / 1e9; }

static int mm_trusted(void) { return AXIsProcessTrusted() ? 1 : 0; }
static int mm_post_access(void) { return CGPreflightPostEventAccess() ? 1 : 0; }
// only asks whether it is granted: never CGRequestScreenCaptureAccess
static int mm_capture_access(void) { return CGPreflightScreenCaptureAccess() ? 1 : 0; }

static void mm_mouse(int type, double x, double y) {
	CGEventRef e = CGEventCreateMouseEvent(NULL, (CGEventType)type, CGPointMake(x, y), kCGMouseButtonLeft);
	if (e != NULL) {
		CGEventPost(kCGHIDEventTap, e);
		CFRelease(e);
	}
}

static void mm_key(int code, int down) {
	CGEventRef e = CGEventCreateKeyboardEvent(NULL, (CGKeyCode)code, down != 0);
	if (e != NULL) {
		CGEventPost(kCGHIDEventTap, e);
		CFRelease(e);
	}
}

static CFTypeRef mm_attr(AXUIElementRef e, const char *name) {
	CFStringRef n = CFStringCreateWithCString(NULL, name, kCFStringEncodingUTF8);
	CFTypeRef v = NULL;
	AXError err = AXUIElementCopyAttributeValue(e, n, &v);
	CFRelease(n);
	if (err != kAXErrorSuccess) {
		return NULL;
	}
	return v;
}

// a string attribute's text, '|' and line ends made spaces ("" when none)
static void mm_str(CFTypeRef v, char *out, int n) {
	out[0] = 0;
	if (v == NULL || CFGetTypeID(v) != CFStringGetTypeID()) {
		return;
	}
	if (!CFStringGetCString((CFStringRef)v, out, n, kCFStringEncodingUTF8)) {
		out[0] = 0;
	}
	for (char *p = out; *p; p++) {
		if (*p == '|' || *p == '\n' || *p == '\r') {
			*p = ' ';
		}
	}
}

static void mm_named(AXUIElementRef e, const char *name, char *out, int n) {
	CFTypeRef v = mm_attr(e, name);
	mm_str(v, out, n);
	if (v != NULL) {
		CFRelease(v);
	}
}

static int mm_rect(AXUIElementRef e, double *r) {
	CFTypeRef p = mm_attr(e, "AXPosition"), s = mm_attr(e, "AXSize");
	CGPoint pt;
	CGSize sz;
	int ok = 0;
	if (p != NULL && s != NULL && CFGetTypeID(p) == AXValueGetTypeID() && CFGetTypeID(s) == AXValueGetTypeID() &&
	    AXValueGetValue((AXValueRef)p, kAXValueTypeCGPoint, &pt) && AXValueGetValue((AXValueRef)s, kAXValueTypeCGSize, &sz)) {
		r[0] = pt.x;
		r[1] = pt.y;
		r[2] = sz.width;
		r[3] = sz.height;
		ok = 1;
	}
	if (p != NULL) {
		CFRelease(p);
	}
	if (s != NULL) {
		CFRelease(s);
	}
	return ok;
}

static int mm_bool(AXUIElementRef e, const char *name) {
	CFTypeRef v = mm_attr(e, name);
	int b = v != NULL && CFGetTypeID(v) == CFBooleanGetTypeID() && CFBooleanGetValue((CFBooleanRef)v);
	if (v != NULL) {
		CFRelease(v);
	}
	return b;
}

// the app's status item (its menu bar extra), retained; NULL when none
static AXUIElementRef mm_status(int pid) {
	AXUIElementRef app = AXUIElementCreateApplication(pid);
	if (app == NULL) {
		return NULL;
	}
	AXUIElementSetMessagingTimeout(app, 2.0f);
	AXUIElementRef out = NULL;
	CFTypeRef bar = mm_attr(app, "AXExtrasMenuBar");
	if (bar != NULL) {
		CFTypeRef kids = mm_attr((AXUIElementRef)bar, "AXChildren");
		if (kids != NULL && CFGetTypeID(kids) == CFArrayGetTypeID() && CFArrayGetCount((CFArrayRef)kids) > 0) {
			out = (AXUIElementRef)CFRetain(CFArrayGetValueAtIndex((CFArrayRef)kids, 0));
		}
		if (kids != NULL) {
			CFRelease(kids);
		}
		CFRelease(bar);
	}
	CFRelease(app);
	return out;
}

// set (1) or read (0) whether pid is the active app (AXFrontmost)
static int mm_frontmost(int pid, int set) {
	AXUIElementRef app = AXUIElementCreateApplication(pid);
	if (app == NULL) {
		return 0;
	}
	AXUIElementSetMessagingTimeout(app, 2.0f);
	int out = 0;
	if (set) {
		out = AXUIElementSetAttributeValue(app, CFSTR("AXFrontmost"), kCFBooleanTrue) == kAXErrorSuccess;
	} else {
		out = mm_bool(app, "AXFrontmost");
	}
	CFRelease(app);
	return out;
}

static int mm_status_rect(int pid, double *r) {
	AXUIElementRef s = mm_status(pid);
	if (s == NULL) {
		return 0;
	}
	int ok = mm_rect(s, r);
	CFRelease(s);
	return ok;
}

// presses the status item through the accessibility interface (the
// answer may come only when its menu closes: a short timeout)
static int mm_press(int pid) {
	AXUIElementRef s = mm_status(pid);
	if (s == NULL) {
		return 0;
	}
	AXUIElementSetMessagingTimeout(s, 1.0f);
	CFStringRef press = CFStringCreateWithCString(NULL, "AXPress", kCFStringEncodingUTF8);
	AXError err = AXUIElementPerformAction(s, press);
	CFRelease(press);
	CFRelease(s);
	return err == kAXErrorSuccess || err == kAXErrorCannotComplete;
}

// The view of the item at index in the status item's open menu (that
// item's first child), held for mm_press_held and mm_held_there
// (retained; the one held before is released). 0: there is none.
static AXUIElementRef mm_held = NULL;

static int mm_hold(int pid, int index) {
	if (mm_held != NULL) {
		CFRelease(mm_held);
		mm_held = NULL;
	}
	AXUIElementRef s = mm_status(pid);
	if (s == NULL) {
		return 0;
	}
	CFTypeRef menus = mm_attr(s, "AXChildren");
	if (menus != NULL && CFGetTypeID(menus) == CFArrayGetTypeID()) {
		CFArrayRef ms = (CFArrayRef)menus;
		for (CFIndex i = 0; i < CFArrayGetCount(ms); i++) {
			AXUIElementRef m = (AXUIElementRef)CFArrayGetValueAtIndex(ms, i);
			char role[128];
			mm_named(m, "AXRole", role, sizeof role);
			if (strcmp(role, "AXMenu") != 0) {
				continue;
			}
			CFTypeRef kids = mm_attr(m, "AXChildren");
			if (kids != NULL && CFGetTypeID(kids) == CFArrayGetTypeID() && index >= 0 && index < CFArrayGetCount((CFArrayRef)kids)) {
				AXUIElementRef it = (AXUIElementRef)CFArrayGetValueAtIndex((CFArrayRef)kids, index);
				CFTypeRef ck = mm_attr(it, "AXChildren");
				if (ck != NULL && CFGetTypeID(ck) == CFArrayGetTypeID() && CFArrayGetCount((CFArrayRef)ck) > 0) {
					mm_held = (AXUIElementRef)CFRetain(CFArrayGetValueAtIndex((CFArrayRef)ck, 0));
				}
				if (ck != NULL) {
					CFRelease(ck);
				}
			}
			if (kids != NULL) {
				CFRelease(kids);
			}
			break;
		}
	}
	if (menus != NULL) {
		CFRelease(menus);
	}
	CFRelease(s);
	if (mm_held != NULL) {
		AXUIElementSetMessagingTimeout(mm_held, 2.0f);
	}
	return mm_held != NULL;
}

// presses the held view as VoiceOver does (AXPress): its AXError (0
// success; -1 nothing held)
static int mm_press_held(void) {
	if (mm_held == NULL) {
		return -1;
	}
	return (int)AXUIElementPerformAction(mm_held, CFSTR("AXPress"));
}

// the held view still answers (its VoiceOver label is read)
static int mm_held_there(void) {
	char label[1024];
	if (mm_held == NULL) {
		return 0;
	}
	mm_named(mm_held, "AXDescription", label, sizeof label);
	return label[0] != 0;
}

// a window of pid at a window level (the status item's, a menu's)
static int mm_window(int pid, int status, double *r) {
	int level = status ? CGWindowLevelForKey(kCGStatusWindowLevelKey) : CGWindowLevelForKey(kCGPopUpMenuWindowLevelKey);
	CFArrayRef list = CGWindowListCopyWindowInfo(kCGWindowListOptionOnScreenOnly, kCGNullWindowID);
	int ok = 0;
	if (list == NULL) {
		return 0;
	}
	for (CFIndex i = 0; i < CFArrayGetCount(list) && !ok; i++) {
		CFDictionaryRef d = (CFDictionaryRef)CFArrayGetValueAtIndex(list, i);
		int wpid = -1, wl = -1;
		CFNumberRef p = (CFNumberRef)CFDictionaryGetValue(d, kCGWindowOwnerPID);
		CFNumberRef l = (CFNumberRef)CFDictionaryGetValue(d, kCGWindowLayer);
		CFDictionaryRef b = (CFDictionaryRef)CFDictionaryGetValue(d, kCGWindowBounds);
		CGRect rc;
		if (p != NULL) {
			CFNumberGetValue(p, kCFNumberIntType, &wpid);
		}
		if (l != NULL) {
			CFNumberGetValue(l, kCFNumberIntType, &wl);
		}
		if (wpid == pid && wl == level && b != NULL && CGRectMakeWithDictionaryRepresentation(b, &rc)) {
			r[0] = rc.origin.x;
			r[1] = rc.origin.y;
			r[2] = rc.size.width;
			r[3] = rc.size.height;
			ok = 1;
		}
	}
	CFRelease(list);
	return ok;
}

// the on-screen windows of pid: "layer:w×h" each, space separated
static void mm_windows(int pid, char *buf, int n) {
	buf[0] = 0;
	CFArrayRef list = CGWindowListCopyWindowInfo(kCGWindowListOptionOnScreenOnly, kCGNullWindowID);
	if (list == NULL) {
		return;
	}
	int len = 0;
	for (CFIndex i = 0; i < CFArrayGetCount(list); i++) {
		CFDictionaryRef d = (CFDictionaryRef)CFArrayGetValueAtIndex(list, i);
		int wpid = -1, wl = -1;
		CFNumberRef p = (CFNumberRef)CFDictionaryGetValue(d, kCGWindowOwnerPID);
		CFNumberRef l = (CFNumberRef)CFDictionaryGetValue(d, kCGWindowLayer);
		CFDictionaryRef b = (CFDictionaryRef)CFDictionaryGetValue(d, kCGWindowBounds);
		CGRect rc = CGRectZero;
		if (p != NULL) {
			CFNumberGetValue(p, kCFNumberIntType, &wpid);
		}
		if (l != NULL) {
			CFNumberGetValue(l, kCFNumberIntType, &wl);
		}
		if (wpid != pid) {
			continue;
		}
		if (b != NULL) {
			CGRectMakeWithDictionaryRepresentation(b, &rc);
		}
		int k = snprintf(buf + len, n - len, "%s%d:%.0fx%.0f", len ? " " : "", wl, rc.size.width, rc.size.height);
		if (k < 0 || len + k >= n) {
			break;
		}
		len += k;
	}
	CFRelease(list);
}

static int mm_put(char *buf, int len, int n, const char *line) {
	int k = (int)strlen(line);
	if (len + k >= n) {
		return len;
	}
	memcpy(buf + len, line, k + 1);
	return len + k;
}

// The status item's open menu: "menu|x|y|w|h", then for each item
// "item|role|title|description|enabled|selected|x|y|w|h|child role|child
// description|child enabled|child width|child height" (its first child: a
// view item's view). 0: no open menu.
static int mm_menu(int pid, char *buf, int n) {
	buf[0] = 0;
	AXUIElementRef s = mm_status(pid);
	if (s == NULL) {
		return 0;
	}
	int len = 0, found = 0;
	static char line[4096];
	CFTypeRef menus = mm_attr(s, "AXChildren");
	if (menus != NULL && CFGetTypeID(menus) == CFArrayGetTypeID()) {
		CFArrayRef ms = (CFArrayRef)menus;
		for (CFIndex i = 0; i < CFArrayGetCount(ms) && !found; i++) {
			AXUIElementRef m = (AXUIElementRef)CFArrayGetValueAtIndex(ms, i);
			char role[128];
			mm_named(m, "AXRole", role, sizeof role);
			if (strcmp(role, "AXMenu") != 0) {
				continue;
			}
			found = 1;
			double r[4] = {0, 0, 0, 0};
			mm_rect(m, r);
			snprintf(line, sizeof line, "menu|%.1f|%.1f|%.1f|%.1f\n", r[0], r[1], r[2], r[3]);
			len = mm_put(buf, len, n, line);
			CFTypeRef kids = mm_attr(m, "AXChildren");
			if (kids != NULL && CFGetTypeID(kids) == CFArrayGetTypeID()) {
				CFArrayRef ks = (CFArrayRef)kids;
				for (CFIndex j = 0; j < CFArrayGetCount(ks); j++) {
					AXUIElementRef it = (AXUIElementRef)CFArrayGetValueAtIndex(ks, j);
					char irole[128], title[1024], desc[1024], crole[128], clabel[1024];
					mm_named(it, "AXRole", irole, sizeof irole);
					mm_named(it, "AXTitle", title, sizeof title);
					mm_named(it, "AXDescription", desc, sizeof desc);
					double ir[4] = {0, 0, 0, 0};
					mm_rect(it, ir);
					int en = mm_bool(it, "AXEnabled"), sel = mm_bool(it, "AXSelected");
					int cen = -1;
					double cr[4] = {0, 0, 0, 0};
					crole[0] = 0;
					clabel[0] = 0;
					CFTypeRef ck = mm_attr(it, "AXChildren");
					if (ck != NULL && CFGetTypeID(ck) == CFArrayGetTypeID() && CFArrayGetCount((CFArrayRef)ck) > 0) {
						AXUIElementRef c = (AXUIElementRef)CFArrayGetValueAtIndex((CFArrayRef)ck, 0);
						mm_named(c, "AXRole", crole, sizeof crole);
						mm_named(c, "AXDescription", clabel, sizeof clabel);
						cen = mm_bool(c, "AXEnabled");
						mm_rect(c, cr);
					}
					if (ck != NULL) {
						CFRelease(ck);
					}
					snprintf(line, sizeof line, "item|%s|%s|%s|%d|%d|%.1f|%.1f|%.1f|%.1f|%s|%s|%d|%.1f|%.1f\n", irole, title, desc, en, sel, ir[0], ir[1], ir[2], ir[3], crole, clabel, cen, cr[2], cr[3]);
					len = mm_put(buf, len, n, line);
				}
			}
			if (kids != NULL) {
				CFRelease(kids);
			}
		}
	}
	if (menus != NULL) {
		CFRelease(menus);
	}
	CFRelease(s);
	return found;
}
*/
import "C"

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"maps"
	"math"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/tray/panelfixture"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

const (
	evMouseDown = 1 // kCGEventLeftMouseDown
	evMouseUp   = 2
	evMouseMove = 5
	keyDown     = 125 // the arrows
	keyUp       = 126
	keyReturn   = 36
	keyEscape   = 53
	keySpace    = 49
)

// testActiveMenu is the CI build's test switch that makes a tray helper
// the active app before its menu shows (internal/cli testSwitches).
const testActiveMenu = "AI_USAGE_TEST_ACTIVE_MENU"

func main() {
	if !allowed(os.Getenv) {
		fmt.Fprintln(os.Stderr, "macmenu: refusing to run outside GitHub Actions (GITHUB_ACTIONS=true, RUNNER_TEMP set): it posts mouse and key events to the session")
		os.Exit(2)
	}
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: macmenu drive -exe AI-USAGE -png FILE [-deadline D] | inspect -pid PID -png FILE [-deadline D]")
		os.Exit(2)
	}
	var f Facts
	var err error
	agent := false
	switch os.Args[1] {
	case "drive":
		fs := flag.NewFlagSet("drive", flag.ExitOnError)
		exe := fs.String("exe", "", "the ai-usage executable")
		pic := fs.String("png", "", "the open menu's capture")
		d := fs.Duration("deadline", 240*time.Second, "give up after this, printing what was measured")
		_ = fs.Parse(os.Args[2:])
		giveUpAfter(*d, false)
		f, err = drive(*exe, *pic)
	case "inspect":
		fs := flag.NewFlagSet("inspect", flag.ExitOnError)
		pid := fs.Int("pid", 0, "a tray helper of the running agent")
		pic := fs.String("png", "", "the open menu's capture")
		d := fs.Duration("deadline", 60*time.Second, "give up after this, printing what was measured")
		_ = fs.Parse(os.Args[2:])
		agent = true
		giveUpAfter(*d, true)
		f, err = inspect(*pid, *pic)
	default:
		fmt.Fprintf(os.Stderr, "macmenu: unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
	if err != nil {
		f.Errors = append(f.Errors, err.Error())
	}
	progress.Lock() // the deadline's report and this one never both print
	report(f, agent)
}

// report prints the facts with what is wrong (one JSON object) and ends
// the program: 1 when something is.
func report(f Facts, agent bool) {
	if agent {
		f.Problems = judgeAgent(f)
	} else {
		f.Problems = judge(f, expected(panelfixture.Combined()))
	}
	if f.TimedOut != "" { // first: the rest follows from it
		f.Problems = append([]string{f.TimedOut}, f.Problems...)
	}
	if f.Problems == nil {
		f.Problems = []string{}
	}
	b, _ := json.Marshal(struct {
		Facts
		Summary string `json:"summary"`
		Keys    string `json:"keys,omitempty"`
	}{f, summary(f, agent), map[bool]string{false: keys(f)}[agent]})
	fmt.Println(string(b))
	if len(f.Problems) > 0 {
		os.Exit(1)
	}
	os.Exit(0)
}

// progress is the last step done and a copy of the facts as they were
// then (trace keeps it), for the deadline's report; and the helpers
// started, which the deadline stops.
var progress struct {
	sync.Mutex
	step    string
	facts   Facts
	helpers []*exec.Cmd
}

// giveUpAfter: once d has passed, the facts measured until the last step
// done are printed with a problem naming that step, the helpers started
// are killed, and the program ends (review B3-I1: a hang is a failure that
// says where, not a job that runs to its time-out).
func giveUpAfter(d time.Duration, agent bool) {
	time.AfterFunc(d, func() {
		progress.Lock()
		f := progress.facts
		f.TimedOut = fmt.Sprintf("timed out after %s, in the step after %q (the facts are those up to that one)", d, orNone(progress.step))
		for _, c := range progress.helpers {
			if c.Process != nil {
				_ = c.Process.Kill()
			}
		}
		report(f, agent)
	})
}

// note keeps a copy of f for the deadline's report.
func note(f *Facts, step string) {
	progress.Lock()
	defer progress.Unlock()
	c := *f
	c.Items = append([]Item(nil), f.Items...)
	c.Trace = append([]string(nil), f.Trace...)
	c.Errors = append([]string(nil), f.Errors...)
	c.Reopens = append([]Reopen(nil), f.Reopens...)
	c.Bars = maps.Clone(f.Bars)
	progress.step, progress.facts = step, c
}

// helperProc is a tray helper of the ai-usage executable that macmenu
// started and gave the fixture's view.
type helperProc struct {
	pid  int
	msgs chan trayproto.Msg
	send func(trayproto.Msg) error
	stop func()
	log  string // its standard error
}

// startHelper starts exe's tray helper (its stderr to logPath, env added
// to this environment), gives it the fixture's view and waits until it is
// ready.
func startHelper(exe, logPath string, env ...string) (*helperProc, error) {
	tok := make([]byte, 16)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(tok)
	cmd := exec.Command(exe, "tray-helper")
	cmd.Env = append(append(os.Environ(), trayproto.EnvToken+"="+token), env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	logf, err := os.Create(logPath)
	if err == nil {
		cmd.Stderr = logf
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	progress.Lock()
	progress.helpers = append(progress.helpers, cmd)
	progress.Unlock()
	h := &helperProc{pid: cmd.Process.Pid, msgs: make(chan trayproto.Msg, 64), log: logPath}
	h.send = func(m trayproto.Msg) error { return trayproto.Encode(stdin, m) }
	h.stop = func() {
		_ = trayproto.Encode(stdin, trayproto.Msg{V: trayproto.Version, Type: trayproto.TypeQuit})
		_ = stdin.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
		}
		if logf != nil {
			_ = logf.Close()
		}
	}
	go func() {
		defer close(h.msgs)
		br := bufio.NewReader(stdout)
		for {
			m, err := trayproto.Decode(br)
			if err != nil {
				return
			}
			h.msgs <- m
		}
	}()
	for _, m := range helloAndView(token, panelfixture.Combined()) {
		if err := trayproto.Encode(stdin, m); err != nil {
			h.stop()
			return nil, fmt.Errorf("the helper's input: %w", err)
		}
	}
	if !waitFor(h.msgs, trayproto.TypeReady, 30*time.Second) {
		h.stop()
		return nil, errors.New("the helper never said it was ready")
	}
	return h, nil
}

// drive starts a tray helper of exe with the fixture's view and opens,
// reads, captures, clicks and drives its menu.
func drive(exe, pic string) (f Facts, err error) {
	if exe == "" || pic == "" {
		return f, errors.New("drive needs -exe and -png")
	}
	base := strings.TrimSuffix(pic, ".png")
	h, err := startHelper(exe, base+"-helper.log")
	if err != nil {
		return f, err
	}
	f.Helper = h.pid
	stopped := false
	defer func() {
		if !stopped {
			h.stop()
		}
	}()
	msgs := h.msgs
	permissions(&f)
	time.Sleep(1500 * time.Millisecond)
	want := expected(panelfixture.Combined())
	pid := f.Helper

	// open, read, capture
	if !openMenu(&f, pid, "open") {
		return f, nil
	}
	f.Windows = windows(pid)
	readMenu(&f, pid)
	match(f.Items, want)
	if img, err := capture(&f, pic); err != nil {
		f.Errors = append(f.Errors, err.Error())
	} else {
		analyse(&f, img, want)
	}

	// a click on Claude's header: nothing is sent, the menu stays open
	if hdr, ok := byID(f.Items, "h.claude"); ok {
		drain(msgs)
		click(hdr.Rect.X+hdr.Rect.W/2, hdr.Rect.Y+hdr.Rect.H/2)
		f.HeaderClick = clicks(msgs, 1500*time.Millisecond)
		f.HeaderStayed = isOpen(&f, pid)
		trace(&f, "header-click")
		if !f.HeaderStayed && !openMenu(&f, pid, "reopen") {
			return f, errors.New("the menu did not open again after the click on the header")
		}
	}

	// the menu's own highlight, for the view's to be compared with: the
	// pointer over Open dashboard
	dash, hasDash := byID(f.Items, "dashboard")
	if hasDash && f.Capture != "" {
		move(dash.Rect.X+dash.Rect.W/2, dash.Rect.Y+dash.Rect.H/2)
		time.Sleep(700 * time.Millisecond)
		g := Facts{Menu: f.Menu}
		if img, err := capture(&g, base+"-native.png"); err == nil {
			bg := bgOf(img, f.Items, "quit", f.Menu, g.Scale)
			if s, ok := highlightShape(img, inPicture(dash.Rect, f.Menu, g.Scale), bg, g.Scale); ok {
				f.NativeHL = &s
			}
		} else {
			f.Errors = append(f.Errors, err.Error())
		}
	}

	// a click on Claude's 5-hour
	row, ok := byID(f.Items, "r.claude.0")
	weekly, ok2 := byID(f.Items, "r.claude.1")
	if !ok || !ok2 || !hasDash {
		closeMenu(&f, pid)
		return f, errors.New("no 5-hour or Weekly row or Open dashboard to click")
	}
	drain(msgs)
	click(row.Rect.X+row.Rect.W/2, row.Rect.Y+row.Rect.H/2)
	f.Click = clicks(msgs, 3*time.Second)
	f.ClickClosed = closed(&f, pid, 3*time.Second)
	trace(&f, "click")
	if !f.ClickClosed {
		closeMenu(&f, pid)
	}

	// Down, then Return: the first row is highlighted and chosen
	if !again(&f, pid, msgs, "keys") {
		return f, errors.New("the menu did not open again for the keys")
	}
	press(keyDown, 60*time.Millisecond)
	time.Sleep(600 * time.Millisecond)
	after := Facts{WinSeen: f.WinSeen}
	readMenu(&after, pid)
	f.Errors = append(f.Errors, after.Errors...)
	match(after.Items, want)
	f.DownSelected = selected(after.Items)
	if img, err := capture(&after, base+"-down.png"); err == nil {
		hdr, ok1 := byID(after.Items, "h.claude")
		hl, ok2 := byID(after.Items, "r.claude.0")
		if ok1 && ok2 {
			at := func(r Rect) string {
				x := int(math.Round((r.X - after.Menu.X + 9) * after.Scale))
				y := int(math.Round((r.Y - after.Menu.Y + r.H/2) * after.Scale))
				return hex6(img, x, y)
			}
			f.Highlight, f.HeaderBg = at(hl.Rect), at(hdr.Rect)
			bg := bgOf(img, after.Items, "quit", after.Menu, after.Scale)
			if s, ok := highlightShape(img, inPicture(hl.Rect, after.Menu, after.Scale), bg, after.Scale); ok {
				f.ViewHL = &s
			}
		}
	} else {
		f.Errors = append(f.Errors, err.Error())
	}
	f.Return, f.ReturnClosed = choose(&f, pid, msgs, keyReturn, "return")

	// Return on the second row (Down twice), on the row Up reaches from
	// Open dashboard, on the row under the pointer (review A3-I1)
	if !again(&f, pid, msgs, "down2") {
		return f, errors.New("the menu did not open again for Down, Down")
	}
	press(keyDown, 60*time.Millisecond)
	time.Sleep(400 * time.Millisecond)
	press(keyDown, 60*time.Millisecond)
	time.Sleep(500 * time.Millisecond)
	f.Down2, f.Down2Closed = choose(&f, pid, msgs, keyReturn, "down2-return")

	if !again(&f, pid, msgs, "up") {
		return f, errors.New("the menu did not open again for Up")
	}
	move(dash.Rect.X+dash.Rect.W/2, dash.Rect.Y+dash.Rect.H/2)
	time.Sleep(700 * time.Millisecond)
	press(keyUp, 60*time.Millisecond)
	time.Sleep(500 * time.Millisecond)
	f.UpSelected = selectedNow(&f, pid, want)
	f.Up, f.UpClosed = choose(&f, pid, msgs, keyReturn, "up-return")

	if !again(&f, pid, msgs, "pointer") {
		return f, errors.New("the menu did not open again for the pointer")
	}
	move(weekly.Rect.X+weekly.Rect.W/2, weekly.Rect.Y+weekly.Rect.H/2)
	time.Sleep(700 * time.Millisecond)
	f.Pointer, f.PointerClosed = choose(&f, pid, msgs, keyReturn, "pointer-return")

	// Down, then Escape: nothing is chosen
	if !again(&f, pid, msgs, "escape") {
		return f, errors.New("the menu did not open again for Escape")
	}
	press(keyDown, 60*time.Millisecond)
	time.Sleep(500 * time.Millisecond)
	press(keyEscape, 60*time.Millisecond)
	f.Escape = clicks(msgs, 2*time.Second)
	f.EscapeClosed = closed(&f, pid, 2*time.Second)
	trace(&f, "escape")
	if !f.EscapeClosed {
		closeMenu(&f, pid)
	}

	// Space on Open dashboard (the menu's own item, under the pointer) and
	// on 5-hour chosen with Down (review A3-M4)
	if !again(&f, pid, msgs, "space-native") {
		return f, errors.New("the menu did not open again for Space")
	}
	move(dash.Rect.X+dash.Rect.W/2, dash.Rect.Y+dash.Rect.H/2)
	time.Sleep(700 * time.Millisecond)
	f.SpaceNative, f.SpaceNativeClosed = choose(&f, pid, msgs, keySpace, "space-native")
	if !again(&f, pid, msgs, "space-view") {
		return f, errors.New("the menu did not open again for Space")
	}
	press(keyDown, 60*time.Millisecond)
	time.Sleep(500 * time.Millisecond)
	f.SpaceView, f.SpaceViewClosed = choose(&f, pid, msgs, keySpace, "space-view")

	// VoiceOver's press on 5-hour, twice back to back in the open menu,
	// then once on the closed menu's view (review N4)
	if !again(&f, pid, msgs, "voiceover") {
		return f, errors.New("the menu did not open again for VoiceOver's press")
	}
	voiceOver(&f, pid, msgs, want)

	// the running helper gets the States view, then the Combined one
	// again: the menu opened after each shows it (review B3-I4)
	for _, fx := range []string{"states", "combined"} {
		in := panelfixture.Combined()
		if fx == "states" {
			in = panelfixture.States()
		}
		f.Reopens = append(f.Reopens, reopen(&f, h, in, fx, base))
	}

	// Return in the menu of a helper made the active app (stage 0's case
	// for the delegate's fall-back: review B3-I2)
	h.stop()
	stopped = true
	f.Active, f.ActiveSwitch = activeReturn(&f, exe, base)
	return f, nil
}

// voiceOver presses 5-hour's view through the accessibility interface, as
// VoiceOver does: twice back to back while the menu is open (one action
// per open: one click, and the menu closes), then once more after it
// closed (the tree keeps a closed menu: nothing may be sent).
func voiceOver(f *Facts, pid int, msgs <-chan trayproto.Msg, want []Expected) {
	g := Facts{WinSeen: f.WinSeen}
	readMenu(&g, pid)
	f.Errors = append(f.Errors, g.Errors...)
	match(g.Items, want)
	at := slices.IndexFunc(g.Items, func(it Item) bool { return it.ID == "r.claude.0" })
	if at < 0 || C.mm_hold(C.int(pid), C.int(at)) == 0 {
		f.AXOpen = "not pressed: no view of 5-hour in the accessibility tree"
		trace(f, "voiceover-none")
		closeMenu(f, pid)
		return
	}
	drain(msgs)
	e1 := C.mm_press_held()
	e2 := C.mm_press_held()
	f.AXOpen = clicks(msgs, 3*time.Second)
	f.AXOpenClosed = closed(f, pid, 2*time.Second)
	trace(f, "voiceover-open")
	if !f.AXOpenClosed {
		closeMenu(f, pid)
	}
	time.Sleep(800 * time.Millisecond)
	drain(msgs)
	f.AXClosedThere = C.mm_held_there() != 0
	e3 := C.mm_press_held()
	f.AXClosed = clicks(msgs, 2*time.Second)
	f.AXErrors = fmt.Sprintf("%d,%d then %d", int(e1), int(e2), int(e3))
	trace(f, "voiceover-closed")
}

// again waits a moment after the last step, forgets what the helper sent
// meanwhile and opens the menu again.
func again(f *Facts, pid int, msgs <-chan trayproto.Msg, step string) bool {
	time.Sleep(800 * time.Millisecond)
	drain(msgs)
	return openMenu(f, pid, step)
}

// choose presses key and says what the helper sent for it (every click:
// exactly one is right) and whether the menu closed; an open menu is
// closed with Escape.
func choose(f *Facts, pid int, msgs <-chan trayproto.Msg, key int, step string) (string, bool) {
	press(key, 300*time.Millisecond)
	got := clicks(msgs, 3*time.Second)
	cl := closed(f, pid, 2*time.Second)
	trace(f, step)
	if !cl {
		closeMenu(f, pid)
	}
	return got, cl
}

// selected is the id (or the title) of the item the menu has selected.
func selected(items []Item) string {
	for _, it := range items {
		if it.Selected {
			if it.ID != "" {
				return it.ID
			}
			return "(" + it.Title + ")"
		}
	}
	return ""
}

// selectedNow reads the open menu and says which item it has selected.
func selectedNow(f *Facts, pid int, want []Expected) string {
	g := Facts{WinSeen: f.WinSeen}
	readMenu(&g, pid)
	f.Errors = append(f.Errors, g.Errors...)
	match(g.Items, want)
	return selected(g.Items)
}

// reopen gives the running helper the view of in, opens its menu, reads
// and captures it (the States view: its problem line and faded bar) and
// closes it.
func reopen(f *Facts, h *helperProc, in tray.MenuInputs, fixture, base string) Reopen {
	r := Reopen{Fixture: fixture}
	if err := h.send(viewOf(in)); err != nil {
		r.Errors = append(r.Errors, "the helper's input: "+err.Error())
		return r
	}
	time.Sleep(1500 * time.Millisecond)
	drain(h.msgs)
	if !openMenu(f, h.pid, "reopen-"+fixture) {
		return r
	}
	r.Opened = true
	g := Facts{WinSeen: f.WinSeen}
	readMenu(&g, h.pid)
	r.Errors = append(r.Errors, g.Errors...)
	r.Menu, r.Items = g.Menu, g.Items
	match(r.Items, expected(in))
	if fixture == "states" {
		if img, err := capture(&g, base+"-states.png"); err != nil {
			r.Errors = append(r.Errors, err.Error())
		} else {
			r.Scale = g.Scale
			analyseStates(&r, img)
		}
	}
	press(keyEscape, 60*time.Millisecond)
	if !closed(f, h.pid, 2*time.Second) {
		closeMenu(f, h.pid)
	}
	trace(f, "reopen-"+fixture+"-closed")
	return r
}

// activeReturn starts a second helper with the CI build's test switch
// that makes it the active app before its menu shows, opens its menu
// once (it becomes active) and again, chooses the first row with Down and
// Return, and says what came: "sent=IDS closed=BOOL", or "not active …"
// when it did not become the active app; and whether the helper said it
// took the switch.
func activeReturn(f *Facts, exe, base string) (string, bool) {
	h, err := startHelper(exe, base+"-active-helper.log", testActiveMenu+"=1")
	if err != nil {
		f.Errors = append(f.Errors, "the second helper: "+err.Error())
		return "", false
	}
	defer h.stop()
	time.Sleep(1500 * time.Millisecond)
	took := false
	if b, err := os.ReadFile(h.log); err == nil {
		took = strings.Contains(string(b), "CI build: test switch "+testActiveMenu+" on")
	}
	C.mm_frontmost(C.int(h.pid), 1)
	if !openMenu(f, h.pid, "active-first") {
		return "the menu did not open", took
	}
	time.Sleep(700 * time.Millisecond)
	press(keyEscape, 60*time.Millisecond)
	closed(f, h.pid, 2*time.Second)
	C.mm_frontmost(C.int(h.pid), 1) // an accessory app by now: it may be made active
	if !again(f, h.pid, h.msgs, "active") {
		return "the menu did not open again", took
	}
	front := C.mm_frontmost(C.int(h.pid), 0) != 0
	press(keyDown, 60*time.Millisecond)
	time.Sleep(500 * time.Millisecond)
	got, cl := choose(f, h.pid, h.msgs, keyReturn, "active-return")
	if !front {
		return fmt.Sprintf("not active (sent=%s closed=%v)", orNone(got), cl), took
	}
	return fmt.Sprintf("sent=%s closed=%v", orNone(got), cl), took
}

// inspect opens the menu of a running helper of the agent, reads and
// captures it, and closes it with Escape.
func inspect(pid int, pic string) (f Facts, err error) {
	if pid <= 0 || pic == "" {
		return f, errors.New("inspect needs -pid and -png")
	}
	f.Helper = pid
	permissions(&f)
	if !openMenu(&f, pid, "open") {
		return f, nil
	}
	f.Windows = windows(pid)
	readMenu(&f, pid)
	if _, err := capture(&f, pic); err != nil {
		f.Errors = append(f.Errors, err.Error())
	}
	press(keyEscape, 60*time.Millisecond)
	f.EscapeClosed = closed(&f, pid, 2*time.Second)
	trace(&f, "escape")
	if !f.EscapeClosed {
		closeMenu(&f, pid)
	}
	return f, nil
}

func permissions(f *Facts) {
	f.AXTrusted = C.mm_trusted() != 0
	f.PostEvents = C.mm_post_access() != 0
	f.ScreenCapture = C.mm_capture_access() != 0
}

func waitFor(msgs <-chan trayproto.Msg, typ string, d time.Duration) bool {
	end := time.After(d)
	for {
		select {
		case m, ok := <-msgs:
			if !ok {
				return false
			}
			if m.Type == typ {
				return true
			}
		case <-end:
			return false
		}
	}
}

func drain(msgs <-chan trayproto.Msg) {
	for {
		select {
		case <-msgs:
		default:
			return
		}
	}
}

// clicks are the ids of the clicks the helper sends, comma separated ("":
// none): the first within d, then every one in the second after it (one
// click or key must send exactly one: review B3-I3).
func clicks(msgs <-chan trayproto.Msg, d time.Duration) string {
	var got []string
	end := time.After(d)
	for {
		select {
		case m, ok := <-msgs:
			if !ok {
				return strings.Join(got, ",")
			}
			if m.Type == trayproto.TypeClick {
				if len(got) == 0 {
					end = time.After(time.Second)
				}
				got = append(got, m.ID)
			}
		case <-end:
			return strings.Join(got, ",")
		}
	}
}

func move(x, y float64) {
	C.mm_mouse(evMouseMove, C.double(x), C.double(y))
}

func click(x, y float64) {
	move(x, y)
	time.Sleep(200 * time.Millisecond)
	C.mm_mouse(evMouseDown, C.double(x), C.double(y))
	time.Sleep(80 * time.Millisecond)
	C.mm_mouse(evMouseUp, C.double(x), C.double(y))
}

func press(code int, hold time.Duration) {
	C.mm_key(C.int(code), 1)
	time.Sleep(hold)
	C.mm_key(C.int(code), 0)
}

func statusRect(pid int) (Rect, bool) {
	var r [4]C.double
	if C.mm_status_rect(C.int(pid), &r[0]) != 0 || C.mm_window(C.int(pid), 1, &r[0]) != 0 {
		return Rect{float64(r[0]), float64(r[1]), float64(r[2]), float64(r[3])}, r[2] > 0 && r[3] > 0
	}
	return Rect{}, false
}

// windows lists pid's on-screen windows ("layer:w×h").
func windows(pid int) string {
	buf := make([]byte, 4096)
	C.mm_windows(C.int(pid), (*C.char)(unsafe.Pointer(&buf[0])), C.int(len(buf)))
	n := 0
	for n < len(buf) && buf[n] != 0 {
		n++
	}
	return string(buf[:n])
}

// isOpen: the menu is open. Two signals: the status item's menu in the
// accessibility tree, and a window of the helper at the pop-up menu
// level. Once such a window has been seen it decides (the tree may keep a
// closed menu); before, the tree does.
func isOpen(f *Facts, pid int) bool {
	buf := make([]byte, 1<<16)
	ax := C.mm_menu(C.int(pid), (*C.char)(unsafe.Pointer(&buf[0])), C.int(len(buf))) != 0
	var r [4]C.double
	win := C.mm_window(C.int(pid), 0, &r[0]) != 0
	if win {
		f.WinSeen = true
	}
	f.last = fmt.Sprintf("ax%d win%d", b2i(ax), b2i(win))
	if f.WinSeen {
		return win
	}
	return ax
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// trace notes a step with the time (the helper's diagnostics use the same
// clock) and the last state seen, and keeps the facts so far for the
// deadline's report.
func trace(f *Facts, step string) {
	f.Trace = append(f.Trace, fmt.Sprintf("%s@%.2f %s", step, float64(C.mm_uptime()), f.last))
	note(f, step)
}

// openMenu clicks the status item (then, if no menu came, presses it
// through the accessibility interface) and waits for the menu.
func openMenu(f *Facts, pid int, step string) bool {
	var r Rect
	var ok bool
	for i := 0; i < 20 && !ok; i++ {
		if r, ok = statusRect(pid); !ok {
			time.Sleep(500 * time.Millisecond)
		}
	}
	if !ok {
		f.Errors = append(f.Errors, "no status item of the helper (accessibility and window list)")
		return false
	}
	f.Status = r
	trace(f, step+"-click")
	click(r.X+r.W/2, r.Y+r.H/2)
	if waitOpen(f, pid, 3*time.Second) {
		f.Opened = "click"
		trace(f, step)
		return true
	}
	if C.mm_press(C.int(pid)) != 0 && waitOpen(f, pid, 3*time.Second) {
		f.Opened = "press"
		trace(f, step+"(press)")
		return true
	}
	trace(f, step+"-failed")
	f.Opened = ""
	return false
}

func waitOpen(f *Facts, pid int, d time.Duration) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if isOpen(f, pid) {
			time.Sleep(300 * time.Millisecond) // laid out and drawn
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}

func closed(f *Facts, pid int, d time.Duration) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if !isOpen(f, pid) {
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}

func closeMenu(f *Facts, pid int) {
	press(keyEscape, 60*time.Millisecond)
	closed(f, pid, 2*time.Second)
	trace(f, "close")
}

// readMenu reads the open menu's items through the accessibility
// interface (the menu's window when it gives no frame).
func readMenu(f *Facts, pid int) {
	buf := make([]byte, 1<<16)
	if C.mm_menu(C.int(pid), (*C.char)(unsafe.Pointer(&buf[0])), C.int(len(buf))) == 0 {
		f.Errors = append(f.Errors, "the open menu is not in the accessibility tree")
		return
	}
	n := 0
	for n < len(buf) && buf[n] != 0 {
		n++
	}
	f.Items = nil
	for _, line := range strings.Split(string(buf[:n]), "\n") {
		parts := strings.Split(line, "|")
		num := func(i int) float64 {
			v, _ := strconv.ParseFloat(parts[i], 64)
			return v
		}
		switch {
		case parts[0] == "menu" && len(parts) == 5:
			f.Menu = Rect{num(1), num(2), num(3), num(4)}
		case parts[0] == "item" && len(parts) == 15:
			f.Items = append(f.Items, Item{Role: parts[1], Title: parts[2], Label: parts[3], Enabled: parts[4] == "1", Selected: parts[5] == "1",
				Rect: Rect{num(6), num(7), num(8), num(9)}, ChildRole: parts[10], ChildLabel: parts[11], ChildEnabled: parts[12] == "1", ChildW: num(13), ChildH: num(14)})
		}
	}
	if f.Menu.W <= 0 || f.Menu.H <= 0 {
		var r [4]C.double
		if C.mm_window(C.int(pid), 0, &r[0]) != 0 {
			f.Menu = Rect{float64(r[0]), float64(r[1]), float64(r[2]), float64(r[3])}
		}
	}
}

// capture takes the menu's picture with screencapture (at the display's
// pixels per point).
func capture(f *Facts, path string) (image.Image, error) {
	if f.Menu.W <= 0 || f.Menu.H <= 0 {
		return nil, errors.New("no frame of the open menu to capture")
	}
	rect := fmt.Sprintf("%d,%d,%d,%d", int(math.Floor(f.Menu.X)), int(math.Floor(f.Menu.Y)), int(math.Ceil(f.Menu.W)), int(math.Ceil(f.Menu.H)))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "screencapture", "-x", "-R", rect, path).CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("screencapture: timed out after 15s")
		}
		return nil, fmt.Errorf("screencapture: %v %s", err, strings.TrimSpace(string(out)))
	}
	r, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	img, err := png.Decode(r)
	if err != nil {
		return nil, fmt.Errorf("the capture: %w", err)
	}
	f.Capture = path
	f.CaptureSize = fmt.Sprintf("%dx%d", img.Bounds().Dx(), img.Bounds().Dy())
	f.Scale = float64(img.Bounds().Dx()) / math.Ceil(f.Menu.W)
	return img, nil
}

func hex6(img image.Image, x, y int) string {
	if !(image.Point{x, y}.In(img.Bounds())) {
		return ""
	}
	return hexColour(img.At(x, y))
}
