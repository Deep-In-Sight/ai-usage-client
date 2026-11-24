//go:build !windows

package trayhelper

import "github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"

// removeStaleIcons: only a Windows notification icon outlives its process.
func removeStaleIcons([]trayproto.StaleIcon) {}

// iconWindow: only Windows identifies an icon by its window.
func iconWindow() uint64 { return 0 }
