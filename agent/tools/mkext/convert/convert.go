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
// Every other line is copied unchanged, and the GNOME 42 form starts with
// 'use strict'; (ES modules are always strict). What the GNOME 42 form
// cannot run is refused too: in the default class (which has no base class
// there) super and the Extension methods (getSettings, openPreferences,
// initTranslations, gettext, ngettext, pgettext, getLogger); any other use
// of Extension; await outside a function (the 42 form is a script); and a
// file whose braces, quotes or comments do not balance. The default class
// ends where its brace closes, found by a scan that skips strings,
// template text and comments (regular expression literals are not read
// as such: one with a quote or a brace is refused or misread).
package convert

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
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
// a constructor's first line; baseUse (in the default class) and
// extensionUse (anywhere) match code of a line that needs the Extension
// base class, which the GNOME 42 form does not have.
var (
	moduleSyntax = regexp.MustCompile(`^\s*(import|export)\b|\bimport\s*[(.]`)
	ctorLine     = regexp.MustCompile(`^\s+constructor\s*\(`)
	baseUse      = regexp.MustCompile(`\bsuper\b|\bthis\s*\.\s*(getSettings|openPreferences|initTranslations|gettext|ngettext|pgettext|getLogger)\b`)
	extensionUse = regexp.MustCompile(`\bExtension\s*[.(]|\bextends\s+Extension\b`)
)

// useStrict is the GNOME 42 form's first line.
const useStrict = "'use strict';"

const extensionImport = `import {Extension} from 'resource:///org/gnome/shell/extensions/extension.js';`

// ToLegacy converts the ES module name (its contents src) to GNOME 42's
// form.
func ToLegacy(name string, src []byte) ([]byte, error) {
	lines := strings.Split(string(src), "\n")
	refuse := func(i int) error { return fmt.Errorf("%s:%d: not a supported form", name, i+1) }
	info, bad := scan(lines)
	if bad >= 0 {
		return nil, refuse(bad)
	}
	out := make([]string, 0, len(lines)+11)
	out = append(out, useStrict)
	// the default class, the brace depth its line starts at and its line
	class, classDepth, classLine := "", -1, -1
	for i, l := range lines {
		code := info[i].code
		inClass := classLine >= 0 && i > classLine && info[i].depth > classDepth
		if inClass && (ctorLine.MatchString(code) || baseUse.MatchString(code)) {
			return nil, refuse(i)
		}
		if info[i].topAwait {
			return nil, refuse(i)
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
			if class != "" || info[i].depth != 0 {
				return nil, refuse(i)
			}
			class, classDepth, classLine = m[1], info[i].depth, i
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
		if moduleSyntax.MatchString(l) || extensionUse.MatchString(code) {
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

// lineInfo is what scan finds on one line of a module.
type lineInfo struct {
	code     string // the line with strings, template text and comments blanked
	depth    int    // the brace depth at the line's start
	topAwait bool   // an await outside every function
}

// brace kinds on scan's stack
const (
	braceBlock    = iota // a class or object body, a plain or control block
	braceFunction        // a function's, method's or arrow function's body
	braceTemplate        // ${ in a template literal
)

// controlWords are the words whose "(…) {" opens a block, not a function.
var controlWords = map[string]bool{"if": true, "for": true, "while": true, "switch": true, "catch": true, "with": true}

var lastWord = regexp.MustCompile(`([\w$]+)\s*$`)

// scan reads a module's lines: per line its code, brace depth and whether
// an await is outside every function. bad is the 0-based line of a string
// left open at a line's end, or of the first brace, comment or template
// left open at the end, or of a "}" without its "{"; -1 when it balances.
func scan(lines []string) (info []lineInfo, bad int) {
	const (
		inCode = iota
		inSingle
		inDouble
		inTemplate
		inLineComment
		inBlockComment
	)
	type brace struct {
		kind, line int
	}
	info = make([]lineInfo, len(lines))
	var stack []brace
	mode, openedAt := inCode, 0
	// stmt is the code since the last ";", "{" or "}" (strings as ""): what
	// a "{" follows
	var stmt strings.Builder
	inFunction := func() bool {
		for _, b := range stack {
			if b.kind == braceFunction {
				return true
			}
		}
		return false
	}
	depth := func() int {
		n := 0
		for _, b := range stack {
			if b.kind != braceTemplate {
				n++
			}
		}
		return n
	}
	for i, l := range lines {
		info[i].depth = depth()
		code := []rune(l)
		word, prev := "", ' '
		endWord := func() {
			if word == "await" && prev != '.' && !inFunction() {
				info[i].topAwait = true
			}
			word = ""
		}
		for j := 0; j < len(code); j++ {
			c := code[j]
			next := rune(0)
			if j+1 < len(code) {
				next = code[j+1]
			}
			switch mode {
			case inLineComment:
				code[j] = ' '
				continue
			case inBlockComment:
				code[j] = ' '
				if c == '*' && next == '/' {
					code[j+1] = ' '
					j++
					mode = inCode
				}
				continue
			case inSingle, inDouble:
				code[j] = ' '
				if c == '\\' && j+1 < len(code) {
					code[j+1] = ' '
					j++
				} else if (c == '\'' && mode == inSingle) || (c == '"' && mode == inDouble) {
					mode = inCode
				}
				continue
			case inTemplate:
				code[j] = ' '
				switch {
				case c == '\\' && j+1 < len(code):
					code[j+1] = ' '
					j++
				case c == '`':
					mode = inCode
				case c == '$' && next == '{':
					code[j+1] = ' '
					j++
					stack = append(stack, brace{braceTemplate, i})
					mode = inCode
				}
				continue
			}
			// code
			if c == '_' || c == '$' || unicode.IsLetter(c) || unicode.IsDigit(c) {
				word += string(c)
				stmt.WriteRune(c)
				continue
			}
			endWord()
			switch {
			case c == '/' && next == '/':
				code[j], code[j+1] = ' ', ' '
				j++
				mode = inLineComment
				continue
			case c == '/' && next == '*':
				code[j], code[j+1] = ' ', ' '
				j++
				mode, openedAt = inBlockComment, i
				continue
			case c == '\'':
				code[j] = ' '
				mode, openedAt = inSingle, i
				stmt.WriteString(`""`)
				continue
			case c == '"':
				code[j] = ' '
				mode, openedAt = inDouble, i
				stmt.WriteString(`""`)
				continue
			case c == '`':
				code[j] = ' '
				mode, openedAt = inTemplate, i
				stmt.WriteString(`""`)
				continue
			case c == '{':
				stack = append(stack, brace{braceKind(stmt.String()), i})
				stmt.Reset()
			case c == '}':
				if len(stack) == 0 {
					return info, i
				}
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				stmt.Reset()
				if top.kind == braceTemplate {
					code[j] = ' '
					mode = inTemplate
					continue
				}
			case c == ';':
				stmt.Reset()
			default:
				stmt.WriteRune(c)
			}
			if !unicode.IsSpace(c) {
				prev = c
			}
		}
		endWord()
		switch mode {
		case inLineComment:
			mode = inCode
		case inSingle, inDouble:
			return info, i // a string ends on its line
		}
		stmt.WriteRune(' ')
		info[i].code = string(code)
	}
	switch {
	case mode != inCode:
		return info, openedAt
	case len(stack) > 0:
		return info, stack[0].line
	}
	return info, -1
}

// braceKind is the kind of the brace that follows stmt: a function body
// after "=>" or after "(…)" that does not follow a control word.
func braceKind(stmt string) int {
	s := strings.TrimSpace(stmt)
	if strings.HasSuffix(s, "=>") {
		return braceFunction
	}
	if !strings.HasSuffix(s, ")") {
		return braceBlock
	}
	n := 0
	for k := len(s) - 1; k >= 0; k-- {
		switch s[k] {
		case ')':
			n++
		case '(':
			n--
			if n == 0 {
				m := lastWord.FindStringSubmatch(s[:k])
				if m != nil && controlWords[m[1]] {
					return braceBlock
				}
				return braceFunction
			}
		}
	}
	return braceBlock
}
