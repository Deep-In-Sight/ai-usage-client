package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// source is a small extension source folder.
func source(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "ai-usage-panel@localhost")
	must(t, os.Mkdir(src, 0o755))
	for name, body := range map[string]string{
		"metadata.json":  "{\n  \"uuid\": \"ai-usage-panel@localhost\",\n  \"shell-version\": [\"46\", \"50\"]\n}\n",
		"extension.js":   "import St from 'gi://St';\n\nexport default class E extends Extension {\n    enable() {\n    }\n}\n",
		"view.js":        "export const PROTOCOL = 1;\n",
		"stylesheet.css": ".a {}\n",
	} {
		must(t, os.WriteFile(filepath.Join(src, name), []byte(body), 0o644))
	}
	return src
}

func TestWriteThenCheck(t *testing.T) {
	src, out := source(t), t.TempDir()
	var w bytes.Buffer
	if ok, err := check(src, out, &w); err != nil || ok {
		t.Fatalf("nothing written yet: %v %v", ok, err)
	}
	if !strings.Contains(w.String(), "missing: ") || !strings.Contains(w.String(), "go run ./tools/mkext --write") {
		t.Fatalf("%s", w.String())
	}
	must(t, write(src, out))
	w.Reset()
	if ok, err := check(src, out, &w); err != nil || !ok || w.Len() != 0 {
		t.Fatalf("after write: %v %v %s", ok, err, w.String())
	}
	b, err := os.ReadFile(filepath.Join(out, "gnome42", "ai-usage-panel@localhost", "extension.js"))
	must(t, err)
	if !strings.Contains(string(b), "const St = imports.gi.St;") || !strings.Contains(string(b), "function init(meta)") {
		t.Fatalf("%s", b)
	}
	b, err = os.ReadFile(filepath.Join(out, "gnome46", "ai-usage-panel@localhost", "extension.js"))
	must(t, err)
	if !strings.HasPrefix(string(b), "import St from 'gi://St';") {
		t.Fatalf("%s", b)
	}
}

// A changed byte in the source, an extra and a missing file all fail the
// check with a diff.
func TestCheckShowsTheDifference(t *testing.T) {
	src, out := source(t), t.TempDir()
	must(t, write(src, out))
	must(t, os.WriteFile(filepath.Join(src, "view.js"), []byte("export const PROTOCOL = 2;\n"), 0o644))
	var w bytes.Buffer
	if ok, err := check(src, out, &w); err != nil || ok {
		t.Fatalf("%v %v", ok, err)
	}
	for _, want := range []string{"gnome42/ai-usage-panel@localhost/view.js (committed)", "-1: var PROTOCOL = 1;", "+1: var PROTOCOL = 2;",
		"-1: export const PROTOCOL = 1;", "+1: export const PROTOCOL = 2;"} {
		if !strings.Contains(w.String(), filepath.FromSlash(want)) && !strings.Contains(w.String(), want) {
			t.Fatalf("no %q in\n%s", want, w.String())
		}
	}
	must(t, write(src, out))
	extra := filepath.Join(out, "gnome46", "ai-usage-panel@localhost", "old.js")
	must(t, os.WriteFile(extra, []byte("x"), 0o644))
	must(t, os.Remove(filepath.Join(out, "gnome42", "ai-usage-panel@localhost", "stylesheet.css")))
	w.Reset()
	if ok, _ := check(src, out, &w); ok {
		t.Fatal("ok with an extra and a missing file")
	}
	if !strings.Contains(w.String(), "not generated: "+extra) || !strings.Contains(w.String(), "missing: ") {
		t.Fatalf("%s", w.String())
	}
	// write removes what is not generated
	must(t, write(src, out))
	if _, err := os.Stat(extra); err == nil {
		t.Fatal("write kept an extra file")
	}
}

func TestCheckPrintsAtMost40Lines(t *testing.T) {
	src, out := source(t), t.TempDir()
	must(t, write(src, out))
	var long strings.Builder
	for range 60 {
		long.WriteString("const a = 1;\n")
	}
	must(t, os.WriteFile(filepath.Join(src, "view.js"), []byte(long.String()), 0o644))
	var w bytes.Buffer
	if ok, _ := check(src, out, &w); ok {
		t.Fatal("ok")
	}
	lines := strings.Split(strings.TrimSpace(w.String()), "\n")
	if len(lines) > 1+maxDiff+2 || !strings.Contains(w.String(), "more lines") {
		t.Fatalf("%d lines:\n%s", len(lines), w.String())
	}
}

func TestSourceWithAFolderIsRefused(t *testing.T) {
	src, out := source(t), t.TempDir()
	must(t, os.Mkdir(filepath.Join(src, "sub"), 0o755))
	if err := write(src, out); err == nil {
		t.Fatal("a folder in the source")
	}
	must(t, os.Remove(filepath.Join(src, "sub")))
	must(t, os.WriteFile(filepath.Join(src, "extension.js"), []byte("import X from './x.js';\n"), 0o644))
	if err := write(src, out); err == nil || !strings.Contains(err.Error(), "extension.js:1: not a supported form") {
		t.Fatalf("%v", err)
	}
}
