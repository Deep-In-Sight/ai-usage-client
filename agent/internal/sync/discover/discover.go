// Package discover decides which server addresses to try, in order, and
// resolves the server's mDNS name on Linux, where a static executable does
// not go through the OS's mDNS resolver.
package discover

import "github.com/Deep-In-Sight/claude-usage-tray-indicator/agent/internal/config"

// Name is the mDNS name the server announces.
const Name = "claude-usage.local"

// MDNSURLs are the server's addresses by name (port 80 and 8700).
var MDNSURLs = []string{"http://claude-usage.local", "http://claude-usage.local:8700"}

// Candidates lists server URLs to try: the last good one, the mDNS names,
// the ones the server announced, then the ones from the installer. Entries
// are normalized and de-duplicated in order; unreadable ones are skipped.
func Candidates(lastGood string, announced, installed []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(list ...string) {
		for _, u := range list {
			n, err := config.NormalizeURL(u)
			if err != nil || seen[n] {
				continue
			}
			seen[n] = true
			out = append(out, n)
		}
	}
	if lastGood != "" {
		add(lastGood)
	}
	add(MDNSURLs...)
	add(announced...)
	add(installed...)
	return out
}
