// Package convert makes the usage panel's GNOME Shell extension in each
// form the agent embeds from its one source (design 6.4; plan Task 11,
// PR-19): the source's ES modules as they are for GNOME 46 and 50, and the
// GNOME 42 form (the shell's old `imports` modules, `init(meta)`).
//
// The conversion is line by line and knows only these source forms; any
// other line with import or export, and a constructor in the default
// class, is refused with its file and line:
//
//	import X from 'gi://X';                       const X = imports.gi.X;
//	import X from 'gi://X?version=N';             imports.gi.versions.X = 'N'; + the same
//	import * as N from 'resource:///org/gnome/shell/<ui|misc>/<file>.js';
//	                                              const N = imports.<ui|misc>.<file>;
//	import {Extension} from 'resource:///org/gnome/shell/extensions/extension.js';
//	                                              (removed)
//	import {a, b} from './<file>.js';             const {a, b} = imports.misc.extensionUtils
//	                                                  .getCurrentExtension().imports.<file>;
//	export function f(                            function f(
//	export const X =                              var X =
//	export default class C extends Extension {    class C { + a constructor(meta) that keeps
//	                                              meta's uuid, path, dir and metadata, and
//	                                              function init(meta) at the file's end
//
// Every other line is copied unchanged.
package convert

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Form is one generated form of the extension: its folder, the shell
// versions its metadata.json lists and whether its modules are converted
// to GNOME 42's.
type Form struct {
	Name          string
	ShellVersions []string
	Legacy        bool
}

// Forms are the forms the agent embeds (design 6.4: one ES-module form
// loads on 46 and 50, stage-0 Z2).
var Forms = []Form{
	{Name: "gnome42", ShellVersions: []string{"42"}, Legacy: true},
	{Name: "gnome46", ShellVersions: []string{"46", "50"}},
}

// Build is form f's files made from the source's (file name: contents):
// metadata.json with f's shell versions, the .js modules converted for a
// legacy form (copied otherwise), every other file copied.
func Build(f Form, src map[string][]byte) (map[string][]byte, error) {
	if _, ok := src["metadata.json"]; !ok {
		return nil, fmt.Errorf("the source has no metadata.json")
	}
	out := make(map[string][]byte, len(src))
	names := make([]string, 0, len(src))
	for name := range src {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		b := src[name]
		var err error
		switch {
		case name == "metadata.json":
			b, err = Metadata(b, f.ShellVersions)
		case f.Legacy && strings.HasSuffix(name, ".js"):
			b, err = ToLegacy(name, b)
		default:
			b = slices.Clone(b)
		}
		if err != nil {
			return nil, err
		}
		out[name] = b
	}
	return out, nil
}

var shellVersion = regexp.MustCompile(`^(\s*"shell-version"\s*:\s*)\[[^\]]*\](,?)\s*$`)

// Metadata is metadata.json with its "shell-version" line (which must be
// one line, and the only one) listing versions.
func Metadata(src []byte, versions []string) ([]byte, error) {
	lines := strings.Split(string(src), "\n")
	n := 0
	for i, l := range lines {
		if strings.Contains(l, `"shell-version"`) {
			m := shellVersion.FindStringSubmatch(l)
			if m == nil {
				return nil, fmt.Errorf("metadata.json:%d: shell-version is not one line", i+1)
			}
			q := make([]string, len(versions))
			for j, v := range versions {
				q[j] = fmt.Sprintf("%q", v)
			}
			lines[i] = m[1] + "[" + strings.Join(q, ", ") + "]" + m[2]
			n++
		}
	}
	if n != 1 {
		return nil, fmt.Errorf("metadata.json: %d shell-version lines, want 1", n)
	}
	out := []byte(strings.Join(lines, "\n"))
	if !json.Valid(out) {
		return nil, fmt.Errorf("metadata.json: not valid JSON")
	}
	return out, nil
}

var (
	giImport     = regexp.MustCompile(`^import (\w+) from 'gi://(\w+)(?:\?version=([\w.]+))?';$`)
	shellImport  = regexp.MustCompile(`^import \* as (\w+) from 'resource:///org/gnome/shell/(ui|misc)/(\w+)\.js';$`)
	localImport  = regexp.MustCompile(`^import (\{\s*\w+(?:\s*,\s*\w+)*\s*,?\s*\}) from '\./(\w+)\.js';$`)
	exportFunc   = regexp.MustCompile(`^export function (\w+\()`)
	exportConst  = regexp.MustCompile(`^export const (\w+ =)`)
	defaultClass = regexp.MustCompile(`^export default class (\w+) extends Extension \{$`)
)

// moduleSyntax matches a line any other module syntax may be on, ctorLine
// a constructor's first line.
var (
	moduleSyntax = regexp.MustCompile(`^\s*(import|export)\b|\bimport\s*[(.]`)
	ctorLine     = regexp.MustCompile(`^\s+constructor\s*\(`)
)

const extensionImport = `import {Extension} from 'resource:///org/gnome/shell/extensions/extension.js';`

// ToLegacy converts the ES module name (its contents src) to GNOME 42's
// form.
func ToLegacy(name string, src []byte) ([]byte, error) {
	lines := strings.Split(string(src), "\n")
	out := make([]string, 0, len(lines)+10)
	// the default class, and whether a line is between its first line and
	// its closing "}"
	class, inClass := "", false
	refuse := func(i int) error { return fmt.Errorf("%s:%d: not a supported form", name, i+1) }
	for i, l := range lines {
		if inClass {
			if ctorLine.MatchString(l) {
				return nil, refuse(i)
			}
			if l == "}" {
				inClass = false
			}
		}
		if m := giImport.FindStringSubmatch(l); m != nil {
			if m[1] != m[2] {
				return nil, refuse(i)
			}
			if m[3] != "" {
				out = append(out, fmt.Sprintf("imports.gi.versions.%s = '%s';", m[2], m[3]))
			}
			out = append(out, fmt.Sprintf("const %s = imports.gi.%s;", m[1], m[2]))
			continue
		}
		if m := shellImport.FindStringSubmatch(l); m != nil {
			out = append(out, fmt.Sprintf("const %s = imports.%s.%s;", m[1], m[2], m[3]))
			continue
		}
		if l == extensionImport {
			continue
		}
		if m := localImport.FindStringSubmatch(l); m != nil {
			out = append(out, fmt.Sprintf("const %s = imports.misc.extensionUtils.getCurrentExtension().imports.%s;", m[1], m[2]))
			continue
		}
		if m := exportFunc.FindStringSubmatch(l); m != nil {
			out = append(out, "function "+m[1]+l[len(m[0]):])
			continue
		}
		if m := exportConst.FindStringSubmatch(l); m != nil {
			out = append(out, "var "+m[1]+l[len(m[0]):])
			continue
		}
		if m := defaultClass.FindStringSubmatch(l); m != nil {
			if class != "" {
				return nil, refuse(i)
			}
			class, inClass = m[1], true
			out = append(out,
				"class "+class+" {",
				"    constructor(meta) {",
				"        this.uuid = meta.uuid;",
				"        this.path = meta.path;",
				"        this.dir = meta.dir;",
				"        this.metadata = meta.metadata;",
				"    }",
				"")
			continue
		}
		if moduleSyntax.MatchString(l) {
			return nil, refuse(i)
		}
		out = append(out, l)
	}
	s := strings.Join(out, "\n")
	if class != "" {
		s = strings.TrimRight(s, "\n") + "\n\nfunction init(meta) {\n    return new " + class + "(meta);\n}\n"
	}
	return []byte(s), nil
}
