package cli

import (
	"bufio"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/supervisor"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

// fakeHelpers stands in for the tray helper processes in run tests: each
// "process" is a goroutine speaking the helper protocol over pipes. By
// default a helper says ready at its first view (waiting with why, when
// set); it ends with 0 on quit or the end of its input.
type fakeHelpers struct {
	t   *testing.T
	mu  sync.Mutex
	why string // "": ready at once; else "waiting" with this reason
	// autoClick: a ready helper clicks this item at once ("quit": a run
	// that ends as soon as its icon is up)
	autoClick string
	onReady   func(h *fakeHelper)
	started   []*fakeHelper
	spawned   chan *fakeHelper
	fail      error
}

type fakeHelper struct {
	id      provider.ID
	uid     uint32
	inR     *io.PipeReader
	inW     *io.PipeWriter
	outR    *io.PipeReader
	outW    *io.PipeWriter
	exitCh  chan int
	once    sync.Once
	ended   chan struct{} // closed when it ended
	code    int
	mu      sync.Mutex
	views   []trayproto.Msg
	notices []string
	gotView chan struct{}
}

func newFakeHelpers(t *testing.T) *fakeHelpers {
	return &fakeHelpers{t: t, spawned: make(chan *fakeHelper, 32)}
}

func (f *fakeHelpers) spawn(p provider.ID, uid uint32, token string) (supervisor.Proc, error) {
	f.mu.Lock()
	why, fail, auto, onReady := f.why, f.fail, f.autoClick, f.onReady
	f.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	h := &fakeHelper{id: p, uid: uid, exitCh: make(chan int, 1), gotView: make(chan struct{}, 64), ended: make(chan struct{})}
	h.inR, h.inW = io.Pipe()
	h.outR, h.outW = io.Pipe()
	go func() {
		r := bufio.NewReader(h.inR)
		first, err := trayproto.Decode(r)
		if err != nil || first.Token != token {
			h.exit(trayproto.ExitRefused)
			return
		}
		said := false
		for {
			m, err := trayproto.Decode(r)
			if err != nil {
				h.exit(0)
				return
			}
			switch m.Type {
			case trayproto.TypeQuit:
				h.exit(0)
				return
			case trayproto.TypeNotice:
				h.mu.Lock()
				h.notices = append(h.notices, m.Text)
				h.mu.Unlock()
			case trayproto.TypeView:
				h.mu.Lock()
				h.views = append(h.views, m)
				h.mu.Unlock()
				if !said {
					said = true
					if why == "" {
						h.say(trayproto.Msg{Type: trayproto.TypeReady})
						go func() { // its own goroutine: the helper keeps reading
							if onReady != nil {
								onReady(h)
							}
							if auto != "" {
								h.click(auto)
							}
						}()
					} else {
						h.say(trayproto.Msg{Type: trayproto.TypeWaiting, Why: why})
					}
				}
				// after its first answer (review n-4): a test that says
				// "ready" once it saw the view comes after "waiting"
				h.gotView <- struct{}{}
			}
		}
	}()
	f.mu.Lock()
	f.started = append(f.started, h)
	f.mu.Unlock()
	f.spawned <- h
	return h, nil
}

func (h *fakeHelper) Stdin() io.WriteCloser { return h.inW }
func (h *fakeHelper) Stdout() io.Reader     { return h.outR }
func (h *fakeHelper) Wait() (int, error)    { return <-h.exitCh, nil }
func (h *fakeHelper) Kill()                 { h.exit(-1) }

func (h *fakeHelper) exit(code int) {
	h.once.Do(func() {
		h.outW.Close()
		h.inR.Close()
		h.code = code
		close(h.ended)
		h.exitCh <- code
	})
}

func (h *fakeHelper) say(m trayproto.Msg) {
	m.V = trayproto.Version
	_ = trayproto.Encode(h.outW, m)
}

func (h *fakeHelper) click(id string) { h.say(trayproto.Msg{Type: trayproto.TypeClick, ID: id}) }

// lastView is the newest view this helper got.
func (h *fakeHelper) lastView() trayproto.Msg {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.views) == 0 {
		return trayproto.Msg{}
	}
	return h.views[len(h.views)-1]
}

func (h *fakeHelper) noticesGot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.notices...)
}

// next is the next helper started.
func (f *fakeHelpers) next() *fakeHelper {
	f.t.Helper()
	select {
	case h := <-f.spawned:
		return h
	case <-time.After(10 * time.Second):
		f.t.Fatal("no tray helper started")
		return nil
	}
}

// ready is the next helper started, once it has its first view.
func (f *fakeHelpers) ready() *fakeHelper {
	f.t.Helper()
	h := f.next()
	select {
	case <-h.gotView:
	case <-time.After(10 * time.Second):
		f.t.Fatal("the helper got no view")
	}
	return h
}

func (f *fakeHelpers) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.started)
}

// itemText is an item's text in a view ("" when hidden or missing).
func itemText(m trayproto.Msg, id string) string {
	for _, it := range m.Items {
		if it.ID == id && it.Visible {
			return it.Text
		}
	}
	return ""
}
