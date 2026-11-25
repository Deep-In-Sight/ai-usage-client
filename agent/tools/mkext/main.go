// mkext makes the usage panel's GNOME Shell extension forms the Linux
// agent embeds (internal/gnomeext/gnome42 and gnome46) from the one source
// in gnome-extension/ai-usage-panel@localhost (design 6.4), with the rules
// of package convert. Run in agent/:
//
//	go run ./tools/mkext           check: the committed forms are the
//	                               source's (on a difference: the first 40
//	                               lines of it, exit 1)
//	go run ./tools/mkext --write   write them again
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Deep-In-Sight/ai-usage-client/agent/tools/mkext/convert"
)

// maxDiff is how many lines of a difference check prints.
const maxDiff = 40

// readSource is the source folder's files (it has no subfolders).
func readSource(dir string) (map[string][]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			return nil, fmt.Errorf("%s: only plain files are supported", filepath.Join(dir, e.Name()))
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		files[e.Name()] = b
	}
	return files, nil
}

// generate is every form's files by their path under out's form folders:
// "gnome42/ai-usage-panel@localhost/extension.js" and so on.
func generate(src string) (map[string][]byte, error) {
	files, err := readSource(src)
	if err != nil {
		return nil, err
	}
	uuid := filepath.Base(filepath.Clean(src))
	all := map[string][]byte{}
	for _, f := range convert.Forms {
		form, err := convert.Build(f, files)
		if err != nil {
			return nil, err
		}
		for name, b := range form {
			all[f.Name+"/"+uuid+"/"+name] = b
		}
	}
	return all, nil
}

// committed is the files under out's form folders, by the same paths.
func committed(out string) (map[string][]byte, error) {
	all := map[string][]byte{}
	for _, f := range convert.Forms {
		root := filepath.Join(out, f.Name)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) && path == root {
				return nil
			}
			if err != nil || d.IsDir() {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(out, path)
			if err != nil {
				return err
			}
			all[filepath.ToSlash(rel)] = b
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return all, nil
}

// lineDiff prints the lines of a that are not in b ("-") and of b that
// are not in a ("+"), in order (a longest common subsequence).
func lineDiff(w io.Writer, a, b []byte) {
	x, y := strings.Split(string(a), "\n"), strings.Split(string(b), "\n")
	// lcs[i][j]: the common subsequence's length of x[i:] and y[j:]
	lcs := make([][]int, len(x)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			i, j = i+1, j+1
		case i < len(x) && (j == len(y) || lcs[i+1][j] >= lcs[i][j+1]):
			fmt.Fprintf(w, "-%d: %s\n", i+1, x[i])
			i++
		default:
			fmt.Fprintf(w, "+%d: %s\n", j+1, y[j])
			j++
		}
	}
}

// check reports to w how the committed forms under out differ from those
// generated from src, at most maxDiff lines; ok when they do not.
func check(src, out string, w io.Writer) (bool, error) {
	want, err := generate(src)
	if err != nil {
		return false, err
	}
	have, err := committed(out)
	if err != nil {
		return false, err
	}
	var d bytes.Buffer
	paths := make([]string, 0, len(want)+len(have))
	for p := range want {
		paths = append(paths, p)
	}
	for p := range have {
		if _, ok := want[p]; !ok {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	for _, p := range paths {
		g, inWant := want[p]
		c, inHave := have[p]
		switch {
		case !inHave:
			fmt.Fprintf(&d, "missing: %s\n", filepath.Join(out, p))
		case !inWant:
			fmt.Fprintf(&d, "not generated: %s\n", filepath.Join(out, p))
		case !bytes.Equal(g, c):
			fmt.Fprintf(&d, "--- %s (committed)\n+++ generated from %s\n", filepath.Join(out, p), src)
			lineDiff(&d, c, g)
		}
	}
	if d.Len() == 0 {
		return true, nil
	}
	lines := strings.SplitAfter(strings.TrimSuffix(d.String(), "\n"), "\n")
	if len(lines) > maxDiff {
		lines = append(lines[:maxDiff], fmt.Sprintf("\n… %d more lines\n", len(lines)-maxDiff))
	}
	fmt.Fprintf(w, "the extension forms are not the source's; run: go run ./tools/mkext --write\n%s\n", strings.Join(lines, ""))
	return false, nil
}

// write replaces the form folders under out with those generated from src.
func write(src, out string) error {
	all, err := generate(src)
	if err != nil {
		return err
	}
	for _, f := range convert.Forms {
		if err := os.RemoveAll(filepath.Join(out, f.Name)); err != nil {
			return err
		}
	}
	for p, b := range all {
		path := filepath.Join(out, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	doWrite := flag.Bool("write", false, "write the forms again")
	src := flag.String("src", "../gnome-extension/ai-usage-panel@localhost", "the extension's source folder")
	out := flag.String("out", "internal/gnomeext", "the folder that holds the forms")
	flag.Parse()
	if flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	if *doWrite {
		if err := write(*src, *out); err != nil {
			fmt.Fprintln(os.Stderr, "mkext:", err)
			os.Exit(1)
		}
		return
	}
	ok, err := check(*src, *out, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mkext:", err)
		os.Exit(1)
	}
	if !ok {
		os.Exit(1)
	}
}
