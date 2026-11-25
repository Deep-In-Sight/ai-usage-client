// Package gnomeext puts the usage panel's GNOME Shell extension
// (ai-usage-panel@localhost, design 6.4) into the user's extensions
// folder: the form for the running shell, embedded in the Linux agent
// only (embed_linux.go). tools/mkext generates the forms from the source
// in gnome-extension/ai-usage-panel@localhost. Install and the session-bus
// bridge both write with Write (design 6.6).
package gnomeext

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// UUID is the extension's uuid and folder name.
const UUID = "ai-usage-panel@localhost"

// FormFor is the embedded form for a GNOME Shell major version: the old
// imports form for 42, the ES modules for 46 and 50; no other shell.
func FormFor(shellMajor int) (form string, ok bool) {
	switch shellMajor {
	case 42:
		return "gnome42", true
	case 46, 50:
		return "gnome46", true
	}
	return "", false
}

// Dir is the extension's folder in the user's data folder, where GNOME
// Shell looks for it (plan PR-13): $XDG_DATA_HOME when it is absolute,
// else ~/.local/share, then gnome-shell/extensions/ai-usage-panel@localhost.
// Without an absolute home either it is an error, never a folder relative
// to wherever the agent runs.
func Dir(getenv func(string) string, home string) (string, error) {
	data := getenv("XDG_DATA_HOME")
	if !filepath.IsAbs(data) {
		if !filepath.IsAbs(home) {
			return "", errors.New("no user data folder: no absolute XDG_DATA_HOME and no home folder")
		}
		data = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(data, "gnome-shell", "extensions", UUID), nil
}

// absolute refuses a folder that is not absolute (Write and Remove work
// only on Dir's folder).
func absolute(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("%q is not an absolute folder", dir)
	}
	return nil
}

// errNotCurrent ends the walk of a folder that is not the form.
var errNotCurrent = errors.New("not current")

// Current reports whether dir holds exactly form's files: each byte for
// byte, and nothing else.
func Current(dir, form string) (bool, error) {
	want, err := formFiles(form)
	if err != nil {
		return false, err
	}
	seen := 0
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == dir {
			if !d.IsDir() {
				return errNotCurrent
			}
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			for name := range want {
				if strings.HasPrefix(name, rel+"/") {
					return nil
				}
			}
			return errNotCurrent
		}
		b, ok := want[rel]
		if !ok || !d.Type().IsRegular() {
			return errNotCurrent
		}
		got, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, b) {
			return errNotCurrent
		}
		seen++
		return nil
	})
	switch {
	case errors.Is(err, errNotCurrent), errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, err
	}
	return seen == len(want), nil
}

// writeFile writes one file of a new folder and rename swaps the folders
// (tests make them fail).
var (
	writeFile = os.WriteFile
	rename    = os.Rename
)

// Staging folders sit beside extensions/, not in it: the shell scans
// extensions/ and would report a folder there whose name is not its uuid.
const (
	stagingNew = ".ai-usage-panel.new-"
	stagingOld = ".ai-usage-panel.old-"
)

// Write makes dir (Dir's folder) hold the form for shellMajor and reports
// whether it wrote anything: nothing when the folder is current. The new
// folder is written beside extensions/ (gnome-shell/.ai-usage-panel.new-
// <pid>) and swapped in by two renames (the old one goes to .old-<pid>
// and is removed), so the shell never finds half a folder (plan PR-13).
func Write(dir string, shellMajor int) (wrote bool, err error) {
	if err := absolute(dir); err != nil {
		return false, err
	}
	form, ok := FormFor(shellMajor)
	if !ok {
		return false, fmt.Errorf("no extension form for GNOME Shell %d", shellMajor)
	}
	files, err := formFiles(form)
	if err != nil {
		return false, err
	}
	if cur, err := Current(dir, form); err == nil && cur {
		return false, nil
	}
	extensions := filepath.Dir(dir)
	base := filepath.Dir(extensions)
	staging := filepath.Join(base, fmt.Sprintf("%s%d", stagingNew, os.Getpid()))
	old := filepath.Join(base, fmt.Sprintf("%s%d", stagingOld, os.Getpid()))
	// what an earlier run of this process may have left
	if err := errors.Join(os.RemoveAll(staging), os.RemoveAll(old)); err != nil {
		return false, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(staging)
		}
	}()
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return false, err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		path := filepath.Join(staging, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false, err
		}
		if err := writeFile(path, files[name], 0o644); err != nil {
			return false, err
		}
	}
	if err := os.MkdirAll(extensions, 0o755); err != nil {
		return false, err
	}
	hadOld := false
	if _, err := os.Lstat(dir); err == nil {
		if err := rename(dir, old); err != nil {
			return false, err
		}
		hadOld = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if err := rename(staging, dir); err != nil {
		if hadOld {
			_ = rename(old, dir)
		}
		return false, err
	}
	if hadOld {
		_ = os.RemoveAll(old) // a leftover goes with the next Remove
	}
	return true, nil
}

// Remove deletes the extension's folder and any staging folder a write
// left behind (of any process).
func Remove(dir string) error {
	if err := absolute(dir); err != nil {
		return err
	}
	errs := []error{os.RemoveAll(dir)}
	base := filepath.Dir(filepath.Dir(dir))
	entries, err := os.ReadDir(base)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), stagingNew) || strings.HasPrefix(e.Name(), stagingOld) {
			errs = append(errs, os.RemoveAll(filepath.Join(base, e.Name())))
		}
	}
	return errors.Join(errs...)
}
