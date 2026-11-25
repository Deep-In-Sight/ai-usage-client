package gnomeext

import (
	"bytes"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Deep-In-Sight/ai-usage-client/agent/tools/mkext/convert"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// needForms: the forms are embedded in the Linux agent only.
func needForms(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the extension is embedded on Linux only")
	}
}

// extDir is a new user data folder's extension folder (not created).
func extDir(t *testing.T) (dir, gnomeShell string) {
	t.Helper()
	data := t.TempDir()
	dir, err := Dir(func(k string) string {
		if k == "XDG_DATA_HOME" {
			return data
		}
		return ""
	}, "/nowhere")
	must(t, err)
	return dir, filepath.Join(data, "gnome-shell")
}

// onDisk is the files under dir by their slash path.
func onDisk(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	got := map[string][]byte{}
	must(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		got[filepath.ToSlash(rel)] = b
		return err
	}))
	return got
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	must(t, err)
	var n []string
	for _, e := range entries {
		n = append(n, e.Name())
	}
	return n
}

func TestFormFor(t *testing.T) {
	for major, want := range map[int]string{42: "gnome42", 46: "gnome46", 50: "gnome46"} {
		if got, ok := FormFor(major); !ok || got != want {
			t.Fatalf("FormFor(%d) = %q, %v", major, got, ok)
		}
	}
	for _, major := range []int{0, 41, 43, 44, 45, 47, 48, 49, 51} {
		if got, ok := FormFor(major); ok {
			t.Fatalf("FormFor(%d) = %q", major, got)
		}
	}
	if UUID != "ai-usage-panel@localhost" {
		t.Fatal(UUID)
	}
}

// Plan PR-13: $XDG_DATA_HOME when it is absolute, else ~/.local/share.
func TestDirHonoursXDGDataHome(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "XDG_DATA_HOME" {
				return v
			}
			return ""
		}
	}
	home := filepath.Join(string(filepath.Separator)+"home", "bob")
	data := filepath.Join(string(filepath.Separator)+"data", "bob")
	if runtime.GOOS == "windows" {
		home, data = `C:\home\bob`, `C:\data\bob`
	}
	for v, want := range map[string]string{
		data:             filepath.Join(data, "gnome-shell", "extensions", UUID),
		"":               filepath.Join(home, ".local", "share", "gnome-shell", "extensions", UUID),
		"relative/share": filepath.Join(home, ".local", "share", "gnome-shell", "extensions", UUID),
		"./share":        filepath.Join(home, ".local", "share", "gnome-shell", "extensions", UUID),
	} {
		if got, err := Dir(env(v), home); err != nil || got != want {
			t.Fatalf("XDG_DATA_HOME=%q: %s %v, want %s", v, got, err, want)
		}
	}
	// m8: no home and no absolute XDG_DATA_HOME: no folder, never a
	// relative one (it would be under whatever the current folder is)
	for _, c := range []struct{ xdg, home string }{{"", ""}, {"relative/share", ""}, {"", "relative/home"}, {"./share", "."}} {
		if got, err := Dir(env(c.xdg), c.home); err == nil {
			t.Fatalf("XDG_DATA_HOME=%q home=%q: %q, want an error", c.xdg, c.home, got)
		}
	}
	if got, err := Dir(env(data), ""); err != nil || got != filepath.Join(data, "gnome-shell", "extensions", UUID) {
		t.Fatalf("an absolute XDG_DATA_HOME without a home: %q %v", got, err)
	}
}

// m8: Write and Remove refuse a folder that is not absolute and touch
// nothing under the current folder.
func TestWriteAndRemoveRefuseARelativeFolder(t *testing.T) {
	needForms(t)
	cwd := t.TempDir()
	t.Chdir(cwd)
	rel := filepath.Join("share", "gnome-shell", "extensions", UUID)
	if _, err := Write(rel, 46); err == nil {
		t.Fatal("Write wrote to a relative folder")
	}
	must(t, os.MkdirAll(rel, 0o755))
	must(t, os.MkdirAll(filepath.Join("share", "gnome-shell", ".ai-usage-panel.new-1"), 0o755))
	if err := Remove(rel); err == nil {
		t.Fatal("Remove removed a relative folder")
	}
	for _, p := range []string{rel, filepath.Join("share", "gnome-shell", ".ai-usage-panel.new-1")} {
		if st, err := os.Stat(filepath.Join(cwd, p)); err != nil || !st.IsDir() {
			t.Fatalf("%s: %v", p, err)
		}
	}
	if n := names(t, filepath.Join(cwd, rel)); len(n) != 0 {
		t.Fatalf("written: %v", n)
	}
}

func TestWriteThenCurrent(t *testing.T) {
	needForms(t)
	dir, _ := extDir(t)
	cur, err := Current(dir, "gnome46")
	if err != nil || cur {
		t.Fatalf("before: %v %v", cur, err)
	}
	wrote, err := Write(dir, 46)
	if err != nil || !wrote {
		t.Fatalf("Write: %v %v", wrote, err)
	}
	want, err := formFiles("gnome46")
	must(t, err)
	if got := onDisk(t, dir); !reflect.DeepEqual(got, want) {
		t.Fatalf("on disk: %d files, want %d", len(got), len(want))
	}
	for form, want := range map[string]bool{"gnome46": true, "gnome42": false} {
		if cur, err := Current(dir, form); err != nil || cur != want {
			t.Fatalf("Current(%s) = %v %v", form, cur, err)
		}
	}
	if !strings.Contains(string(want["metadata.json"]), `"46", "50"`) || len(want) != 6 {
		t.Fatalf("gnome46: %d files, metadata %s", len(want), want["metadata.json"])
	}
	// a shell of the other form (an Ubuntu upgrade the other way round)
	if wrote, err := Write(dir, 42); err != nil || !wrote {
		t.Fatalf("Write 42: %v %v", wrote, err)
	}
	if cur, _ := Current(dir, "gnome42"); !cur {
		t.Fatal("not the 42 form")
	}
	if !strings.Contains(string(onDisk(t, dir)["extension.js"]), "function init(meta)") {
		t.Fatal("the 42 form has no init")
	}
	if _, err := Write(dir, 44); err == nil {
		t.Fatal("wrote for GNOME 44")
	}
	if _, err := Current(dir, "gnome44"); err == nil {
		t.Fatal("a form gnome44")
	}
}

func TestWriteReplacesStaleFiles(t *testing.T) {
	needForms(t)
	dir, _ := extDir(t)
	_, err := Write(dir, 46)
	must(t, err)
	ext := filepath.Join(dir, "extension.js")
	b, err := os.ReadFile(ext)
	must(t, err)
	b[10] ^= 1
	must(t, os.WriteFile(ext, b, 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "old.js"), []byte("x"), 0o644))
	if cur, _ := Current(dir, "gnome46"); cur {
		t.Fatal("current with a changed byte and an extra file")
	}
	if wrote, err := Write(dir, 46); err != nil || !wrote {
		t.Fatalf("Write: %v %v", wrote, err)
	}
	want, _ := formFiles("gnome46")
	if got := onDisk(t, dir); !reflect.DeepEqual(got, want) {
		t.Fatalf("not fixed: %v", slices.Sorted(maps.Keys(got)))
	}
	// an extra file alone, or an extra folder alone, also makes it not
	// current, and Write removes it
	extra := filepath.Join(dir, "notes.txt")
	must(t, os.WriteFile(extra, []byte("x"), 0o644))
	if cur, _ := Current(dir, "gnome46"); cur {
		t.Fatal("current with an extra file")
	}
	if wrote, err := Write(dir, 46); err != nil || !wrote {
		t.Fatalf("Write: %v %v", wrote, err)
	}
	if _, err := os.Stat(extra); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the extra file is still there: %v", err)
	}
	must(t, os.Mkdir(filepath.Join(dir, "sub"), 0o755))
	if cur, _ := Current(dir, "gnome46"); cur {
		t.Fatal("current with an extra folder")
	}
}

// RF1: a current folder is not written again.
func TestWriteLeavesCurrentAlone(t *testing.T) {
	needForms(t)
	dir, gnomeShell := extDir(t)
	_, err := Write(dir, 50)
	must(t, err)
	old := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, p := range append([]string{dir}, mapKeys(onDisk(t, dir), dir)...) {
		must(t, os.Chtimes(p, old, old))
	}
	wrote, err := Write(dir, 46) // 46 and 50 share the form
	if err != nil || wrote {
		t.Fatalf("Write: %v %v", wrote, err)
	}
	for _, p := range append([]string{dir}, mapKeys(onDisk(t, dir), dir)...) {
		st, err := os.Stat(p)
		must(t, err)
		if !st.ModTime().Equal(old) {
			t.Fatalf("%s was touched", p)
		}
	}
	if n := names(t, gnomeShell); !slices.Equal(n, []string{"extensions"}) {
		t.Fatalf("gnome-shell holds %v", n)
	}
}

func mapKeys(m map[string][]byte, dir string) []string {
	var p []string
	for k := range m {
		p = append(p, filepath.Join(dir, filepath.FromSlash(k)))
	}
	return p
}

// RF1: a write that fails part way leaves the old folder whole and no
// staging folder; nothing but the extension's folder ever appears in
// extensions/ (the shell scans it).
func TestWriteNeverLeavesAHalfFolder(t *testing.T) {
	needForms(t)
	dir, gnomeShell := extDir(t)
	_, err := Write(dir, 42)
	must(t, err)
	before := onDisk(t, dir)
	extensions := filepath.Dir(dir)
	watch, calls := extensions, 0
	writeFile = func(name string, b []byte, perm fs.FileMode) error {
		calls++
		if entries, err := os.ReadDir(watch); err == nil {
			for _, e := range entries {
				if e.Name() != UUID {
					t.Errorf("extensions/ holds %s while writing", e.Name())
				}
			}
		}
		if strings.HasPrefix(name, watch) {
			t.Errorf("a file written in place: %s", name)
		}
		if calls > 3 {
			return errors.New("disk full")
		}
		return os.WriteFile(name, b, perm)
	}
	t.Cleanup(func() { writeFile = os.WriteFile })
	if wrote, err := Write(dir, 46); err == nil || wrote {
		t.Fatalf("Write: %v %v", wrote, err)
	}
	if calls != 4 {
		t.Fatalf("%d files written", calls)
	}
	if got := onDisk(t, dir); !reflect.DeepEqual(got, before) {
		t.Fatal("the old folder changed")
	}
	if n := names(t, gnomeShell); !slices.Equal(n, []string{"extensions"}) {
		t.Fatalf("gnome-shell holds %v", n)
	}
	if n := names(t, extensions); !slices.Equal(n, []string{UUID}) {
		t.Fatalf("extensions/ holds %v", n)
	}
	// the first write into an empty data folder fails the same way
	dir2, gnomeShell2 := extDir(t)
	watch, calls = filepath.Dir(dir2), 0
	if _, err := Write(dir2, 46); err == nil {
		t.Fatal("no error")
	}
	if _, err := os.Stat(dir2); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a half folder: %v", err)
	}
	if n := names(t, gnomeShell2); len(n) > 1 || (len(n) == 1 && n[0] != "extensions") {
		t.Fatalf("gnome-shell holds %v", n)
	}
}

// m7: a symlink is never taken for a file of the form, even to a file with
// the right bytes, nor the folder itself a symlink to a current folder;
// Write puts plain files in their place.
func TestCurrentRefusesSymlinks(t *testing.T) {
	needForms(t)
	dir, _ := extDir(t)
	_, err := Write(dir, 46)
	must(t, err)
	ext := filepath.Join(dir, "extension.js")
	b, err := os.ReadFile(ext)
	must(t, err)
	elsewhere := filepath.Join(t.TempDir(), "extension.js")
	must(t, os.WriteFile(elsewhere, b, 0o644))
	must(t, os.Remove(ext))
	must(t, os.Symlink(elsewhere, ext))
	if cur, err := Current(dir, "gnome46"); err != nil || cur {
		t.Fatalf("a symlinked file is current: %v %v", cur, err)
	}
	if wrote, err := Write(dir, 46); err != nil || !wrote {
		t.Fatalf("Write: %v %v", wrote, err)
	}
	if st, err := os.Lstat(ext); err != nil || !st.Mode().IsRegular() {
		t.Fatalf("not a plain file again: %v %v", st, err)
	}
	// the extension folder itself a symlink to a current folder
	other, _ := extDir(t)
	_, err = Write(other, 46)
	must(t, err)
	dir2, _ := extDir(t)
	must(t, os.MkdirAll(filepath.Dir(dir2), 0o755))
	must(t, os.Symlink(other, dir2))
	if cur, err := Current(dir2, "gnome46"); err != nil || cur {
		t.Fatalf("a symlinked folder is current: %v %v", cur, err)
	}
}

// m7: when the new folder cannot be renamed into place, the old folder,
// already moved aside, is renamed back: the shell keeps the old extension
// whole, and nothing is left beside extensions/.
func TestWriteRollsBackWhenTheSwapFails(t *testing.T) {
	needForms(t)
	dir, gnomeShell := extDir(t)
	_, err := Write(dir, 42)
	must(t, err)
	before := onDisk(t, dir)
	calls := 0
	rename = func(from, to string) error {
		calls++
		if to == dir && strings.HasPrefix(filepath.Base(from), stagingNew) {
			return errors.New("rename refused")
		}
		return os.Rename(from, to)
	}
	t.Cleanup(func() { rename = os.Rename })
	if wrote, err := Write(dir, 46); err == nil || wrote {
		t.Fatalf("Write: %v %v", wrote, err)
	}
	if calls < 2 {
		t.Fatalf("%d renames", calls)
	}
	if got := onDisk(t, dir); !reflect.DeepEqual(got, before) {
		t.Fatal("the old folder is not back whole")
	}
	if n := names(t, gnomeShell); !slices.Equal(n, []string{"extensions"}) {
		t.Fatalf("gnome-shell holds %v", n)
	}
	if n := names(t, filepath.Dir(dir)); !slices.Equal(n, []string{UUID}) {
		t.Fatalf("extensions/ holds %v", n)
	}
}

func TestRemove(t *testing.T) {
	needForms(t)
	dir, gnomeShell := extDir(t)
	_, err := Write(dir, 46)
	must(t, err)
	other := filepath.Join(filepath.Dir(dir), "someone-else@example.com")
	must(t, os.MkdirAll(other, 0o755))
	for _, leftover := range []string{".ai-usage-panel.new-123", ".ai-usage-panel.old-456"} {
		must(t, os.MkdirAll(filepath.Join(gnomeShell, leftover, "x"), 0o755))
	}
	must(t, os.WriteFile(filepath.Join(gnomeShell, ".ai-usage-panel-notes"), nil, 0o644))
	must(t, Remove(dir))
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("still there: %v", err)
	}
	if n := names(t, gnomeShell); !slices.Equal(n, []string{".ai-usage-panel-notes", "extensions"}) {
		t.Fatalf("gnome-shell holds %v", n)
	}
	if n := names(t, filepath.Dir(dir)); !slices.Equal(n, []string{"someone-else@example.com"}) {
		t.Fatalf("extensions/ holds %v", n)
	}
	must(t, Remove(dir)) // nothing left: no error
	nothing, _ := extDir(t)
	must(t, Remove(nothing))
}

// Plan Task 11 with pre-flight F20: the forms the agent embeds are the
// ones tools/mkext makes from the source now, not merely the committed
// folders (the CI step "extension forms are current" checks those).
func TestEmbeddedFormsAreTheGeneratedOnes(t *testing.T) {
	needForms(t)
	srcDir := filepath.Join("..", "..", "..", "gnome-extension", UUID)
	entries, err := os.ReadDir(srcDir)
	must(t, err)
	src := map[string][]byte{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(srcDir, e.Name()))
		must(t, err)
		src[e.Name()] = b
	}
	if len(convert.Forms) != 2 {
		t.Fatalf("%d forms", len(convert.Forms))
	}
	for _, f := range convert.Forms {
		want, err := convert.Build(f, src)
		must(t, err)
		got, err := formFiles(f.Name)
		must(t, err)
		if !reflect.DeepEqual(got, want) {
			for name := range want {
				if !bytes.Equal(got[name], want[name]) {
					t.Errorf("%s/%s differs from the source's; run: go run ./tools/mkext --write", f.Name, name)
				}
			}
			t.Fatalf("%s: %d files embedded, %d generated", f.Name, len(got), len(want))
		}
	}
}

// Outside Linux nothing of the extension is embedded: Write and Current
// fail and write nothing.
func TestNoFormsOutsideLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("Linux embeds the forms")
	}
	dir, gnomeShell := extDir(t)
	if _, err := Write(dir, 46); err == nil {
		t.Fatal("Write worked")
	}
	if _, err := Current(dir, "gnome46"); err == nil {
		t.Fatal("Current worked")
	}
	if _, err := os.Stat(gnomeShell); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("wrote something: %v", err)
	}
}
