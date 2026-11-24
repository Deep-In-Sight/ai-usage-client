package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/icon"
)

// The committed program icon is what the generator draws now.
func TestIconMatchesCommitted(t *testing.T) {
	out := filepath.Join(t.TempDir(), "icon.ico")
	if err := run(out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../winres/icon.ico")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("winres/icon.ico is out of date: run `go run ./tools/mkicon` in agent/")
	}
	if !bytes.Equal(got, icon.ProgramICO()) {
		t.Fatal("mkicon does not write icon.ProgramICO")
	}
}

// The Windows resources use that icon.
func TestWinresUsesTheIcon(t *testing.T) {
	b, err := os.ReadFile("../../winres/winres.json.in")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(`"0000": "icon.ico"`)) {
		t.Fatal(`winres.json.in does not use icon.ico for the APP icon`)
	}
	if _, err := os.Stat("../../winres/icon.png"); err == nil {
		t.Fatal("winres/icon.png (the old Clawd icon) is still there")
	}
}
