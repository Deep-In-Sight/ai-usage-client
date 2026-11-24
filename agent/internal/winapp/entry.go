// Package winapp is the Windows "Installed apps" entry and the console and
// message-box helpers of a GUI executable. On other OSes the calls do
// nothing (EntryFor is the same everywhere, so it is tested everywhere).
package winapp

import (
	"path/filepath"
	"time"
)

// Entry is the HKCU ...\Uninstall\ClaudeUsage key.
type Entry struct {
	DisplayName, DisplayVersion, Publisher, DisplayIcon, UninstallString, QuietUninstallString,
	InstallLocation, InstallDate, URLInfoAbout string
	EstimatedSizeKB    uint32
	NoModify, NoRepair uint32
}

// EntryFor is the entry for the executable at exe.
func EntryFor(exe, ver string, sizeBytes int64, now time.Time) Entry {
	q := `"` + exe + `"`
	return Entry{
		DisplayName:          "Claude Usage",
		DisplayVersion:       ver,
		Publisher:            "Deep-In-Sight",
		DisplayIcon:          q + ",0",
		UninstallString:      q + " uninstall",
		QuietUninstallString: q + " uninstall",
		InstallLocation:      filepath.Dir(exe),
		InstallDate:          now.Format("20060102"),
		URLInfoAbout:         "https://github.com/Deep-In-Sight/ai-usage-client",
		EstimatedSizeKB:      uint32((sizeBytes + 1023) / 1024),
		NoModify:             1,
		NoRepair:             1,
	}
}
