package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestIconMatchesCommitted(t *testing.T) {
	out := filepath.Join(t.TempDir(), "icon.png")
	if err := run("../../../claude-usage@localhost/clawd.svg", out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../winres/icon.png")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("winres/icon.png is out of date: run `go run ./tools/mkicon` in agent/")
	}
}
