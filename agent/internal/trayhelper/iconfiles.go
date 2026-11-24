package trayhelper

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"
)

// On Windows the tray library loads icons from files. Handing it bytes
// would leave one file per distinct icon in %TEMP%, forever; instead the
// agent keeps a few files of its own in the data folder and removes them
// on exit (and uninstall removes the data folder).
const iconsKeep = 8

var iconName = regexp.MustCompile(`^[0-9a-f]{16}\.ico$`)

// iconFile writes img to dir as <hash>.ico unless it is there already, keeps
// at most keep of these files (the most recently used) and returns its path.
// Files with other names are never touched.
func iconFile(dir string, img []byte, keep int) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	h := sha256.Sum256(img)
	p := filepath.Join(dir, hex.EncodeToString(h[:8])+".ico")
	now := time.Now()
	if _, err := os.Stat(p); err == nil {
		_ = os.Chtimes(p, now, now)
	} else if err := os.WriteFile(p, img, 0o600); err != nil {
		return "", err
	}
	type file struct {
		path string
		mod  time.Time
	}
	var files []file
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !iconName.MatchString(e.Name()) || filepath.Join(dir, e.Name()) == p {
			continue
		}
		if fi, err := e.Info(); err == nil {
			files = append(files, file{filepath.Join(dir, e.Name()), fi.ModTime()})
		}
	}
	slices.SortFunc(files, func(a, b file) int { return b.mod.Compare(a.mod) })
	for i := keep - 1; i < len(files); i++ {
		_ = os.Remove(files[i].path)
	}
	return p, nil
}

// removeIconFiles removes this program's icon files from dir, and dir when
// it is then empty.
func removeIconFiles(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if iconName.MatchString(e.Name()) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	_ = os.Remove(dir)
}

// needIcon reports whether the tray icon must be set: its picture changed,
// or (Windows) the notification area's icon size did, after a display-scale
// change, so that the ICO entry of the new size is loaded. size is 0 where
// the icon does not depend on it.
func needIcon(lastImg []byte, lastSize int, img []byte, size int) bool {
	return !bytes.Equal(img, lastImg) || size != lastSize
}
