package tray

import (
	"errors"

	"github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/status"
)

// ErrNoTray: this build has no tray (macOS built without cgo).
var ErrNoTray = errors.New("tray: not compiled into this build")

// Controller is what the tray asks of the running agent.
type Controller interface {
	Snapshot() status.Snapshot
	Refresh()
	DashboardURL() string
	HasToggle() bool
	AutostartOn() bool
	SetAutostart(on bool) error
	Remove() error
}
