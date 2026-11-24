package cli

import (
	"context"
	"errors"
	"os"
	"sync"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/autostart"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/config"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/status"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/sync/engine"
)

// controller is what the tray menu shows and changes of the running agent.
type controller struct {
	mu      sync.Mutex
	snap    status.Snapshot
	cfg     config.Config
	eng     *engine.Engine
	toggle  autostart.Toggler
	exe     string
	offFile string // marker: start at login turned off here (install keeps it off)
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
	if err := c.toggle.SetEnabled(context.Background(), c.exe, on); err != nil {
		return err
	}
	if c.offFile != "" {
		if on {
			_ = os.Remove(c.offFile)
		} else {
			_ = os.WriteFile(c.offFile, nil, 0o600)
		}
	}
	return nil
}
