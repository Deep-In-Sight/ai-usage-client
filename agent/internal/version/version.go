// Package version holds the agent version, set at build time with
// -ldflags "-X github.com/Deep-In-Sight/ai-usage-client/agent/internal/version.Version=1.2.3".
package version

// Version is the agent version.
var Version = "0.0.0-dev"

// UserAgent is the User-Agent header sent on every request.
func UserAgent() string { return "ai-usage/" + Version }
