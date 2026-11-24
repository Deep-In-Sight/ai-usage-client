package winapp

import (
	"testing"
	"time"
)

var day = time.Date(2026, 11, 24, 15, 4, 5, 0, time.UTC)

func eq[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestUninstallEntryQuotesPath(t *testing.T) {
	exe := "C:\\Users\\\ud64d \uae38\ub3d9\\AppData\\Local\\Programs\\ClaudeUsage\\claude-usage.exe"
	e := EntryFor(exe, "1.0.0", 12_000_000, day)
	eq(t, e.UninstallString, `"`+exe+`" uninstall`)
	eq(t, e.QuietUninstallString, `"`+exe+`" uninstall`)
	eq(t, e.DisplayIcon, `"`+exe+`",0`)
	eq(t, e.EstimatedSizeKB, uint32(11719))
	eq(t, e.DisplayName, "Claude Usage")
	eq(t, e.DisplayVersion, "1.0.0")
	eq(t, e.Publisher, "Deep-In-Sight")
	eq(t, e.InstallDate, "20261124")
	eq(t, e.URLInfoAbout, "https://github.com/Deep-In-Sight/ai-usage-client")
	eq(t, e.NoModify, uint32(1))
	eq(t, e.NoRepair, uint32(1))
	eq(t, EntryFor(`C:\x\claude-usage.exe`, "1.0.0", 1024, day).EstimatedSizeKB, uint32(1))
	eq(t, EntryFor(`C:\x\claude-usage.exe`, "1.0.0", 1025, day).EstimatedSizeKB, uint32(2))
}
