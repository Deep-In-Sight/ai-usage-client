package version

import "testing"

func TestUserAgent(t *testing.T) {
	if got := UserAgent(); got != "ai-usage/0.0.0-dev" {
		t.Fatalf("got %q", got)
	}
}
