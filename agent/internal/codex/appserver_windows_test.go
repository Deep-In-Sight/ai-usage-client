package codex

import (
	"context"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/clock"
)

// O10: codex is started with CREATE_NO_WINDOW, so no console window
// flashes when the fallback runs from the tray agent.
func TestCreateNoWindowFlag(t *testing.T) {
	clk := clock.NewFake(t0)
	s, c := appServer(t, clk, fakeCodex(t))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if res, ok := s.Fetch(ctx, authUntil(t0.Add(time.Hour))); !ok || res.HTTPStatus != 200 {
		t.Fatalf("%v %+v", ok, res)
	}
	eq(t, len(c.cmds), 1)
	attr := c.cmds[0].SysProcAttr
	if attr == nil || attr.CreationFlags&0x08000000 == 0 {
		t.Fatalf("creation flags %+v", attr)
	}
}
