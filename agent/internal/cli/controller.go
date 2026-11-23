package cli

import (
	"context"
	"errors"
	"sync"

	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/autostart"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/config"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/status"
	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/sync/engine"
)

// controller is the tray's view of the running agent (tray.Controller).
type controller struct {
	mu     sync.Mutex
	snap   status.Snapshot
	cfg    config.Config
	eng    *engine.Engine
	toggle autostart.Toggler
	exe    string
	remove func() error
}

func newController(cfg config.Config, initial status.Snapshot, eng *engine.Engine, toggle autostart.Toggler) *controller {
	if initial.Mode == "" {
		initial.Mode = engine.ModeStarting
	}
	return &controller{cfg: cfg, snap: initial, eng: eng, toggle: toggle}
}

func (c *controller) set(s status.Snapshot) {
	c.mu.Lock()
	c.snap = s
	c.mu.Unlock()
}

func (c *controller) Snapshot() status.Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snap
}

func (c *controller) Refresh() {
	if c.eng != nil {
		c.eng.RequestFresh()
	}
}

func (c *controller) DashboardURL() string {
	if u := c.Snapshot().ServerURL; u != "" {
		return u
	}
	if len(c.cfg.ServerURLs) > 0 {
		return c.cfg.ServerURLs[0]
	}
	return ""
}

func (c *controller) HasToggle() bool { return c.toggle != nil }

func (c *controller) AutostartOn() bool {
	return c.toggle != nil && c.toggle.Enabled(context.Background())
}

func (c *controller) SetAutostart(on bool) error {
	if c.toggle == nil {
		return errors.New("start at login cannot be changed here")
	}
	return c.toggle.SetEnabled(context.Background(), c.exe, on)
}

func (c *controller) Remove() error {
	if c.remove == nil {
		return errors.New("not available")
	}
	return c.remove()
}
