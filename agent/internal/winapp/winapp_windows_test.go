package winapp

import (
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/windows/registry"
)

func TestEntryRoundTrip(t *testing.T) {
	key := fmt.Sprintf(`Software\ClaudeUsageTest-%d`, os.Getpid())
	t.Cleanup(func() { _ = registry.DeleteKey(registry.CURRENT_USER, key) })
	e := EntryFor(`C:\Users\a b\AppData\Local\Programs\ClaudeUsage\claude-usage.exe`, "1.2.3", 12_000_000, day)
	if err := registerAt(key, e); err != nil {
		t.Fatal(err)
	}
	got, ok := readAt(key)
	if !ok || got != e {
		t.Fatalf("got %+v %v", got, ok)
	}
	if err := unregisterAt(key); err != nil {
		t.Fatal(err)
	}
	if _, ok := readAt(key); ok {
		t.Fatal("still there")
	}
	if err := unregisterAt(key); err != nil {
		t.Fatal(err)
	}
}
