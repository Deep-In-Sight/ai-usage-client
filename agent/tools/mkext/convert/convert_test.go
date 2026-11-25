package convert

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// legacy is src's GNOME 42 form after its first line, which must be the
// strict-mode directive (the ES modules are strict; the 42 form says so).
func legacy(t *testing.T, src string) string {
	t.Helper()
	b, err := ToLegacy("extension.js", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	rest, ok := strings.CutPrefix(string(b), "'use strict';\n")
	if !ok {
		t.Fatalf("the GNOME 42 form does not start with 'use strict': %q", b)
	}
	return rest
}

// One case per row of plan Task 11's table (PR-19).
func TestToLegacyForms(t *testing.T) {
	for _, c := range []struct{ name, src, want string }{
		{"gi import", "import St from 'gi://St';\n", "const St = imports.gi.St;\n"},
		{"gi import with a version", "import Soup from 'gi://Soup?version=3.0';\n",
			"imports.gi.versions.Soup = '3.0';\nconst Soup = imports.gi.Soup;\n"},
		{"shell ui module", "import * as PopupMenu from 'resource:///org/gnome/shell/ui/popupMenu.js';\n",
			"const PopupMenu = imports.ui.popupMenu;\n"},
		{"shell misc module", "import * as Config from 'resource:///org/gnome/shell/misc/config.js';\n",
			"const Config = imports.misc.config;\n"},
		{"the Extension import", "import {Extension} from 'resource:///org/gnome/shell/extensions/extension.js';\nconst a = 1;\n",
			"const a = 1;\n"},
		{"a module of the extension", "import {PROTOCOL, parseView} from './view.js';\n",
			"const {PROTOCOL, parseView} = imports.misc.extensionUtils.getCurrentExtension().imports.view;\n"},
		{"exported function", "export function fillWidth(percent, track) {\n    return 0;\n}\n",
			"function fillWidth(percent, track) {\n    return 0;\n}\n"},
		{"exported constant", "export const PROTOCOL = 1;\n", "var PROTOCOL = 1;\n"},
		{"other lines", "// import and export are words\nconst s = `export x`;\n    if (a)\n\n", "// import and export are words\nconst s = `export x`;\n    if (a)\n\n"},
	} {
		if got := legacy(t, c.src); got != c.want {
			t.Fatalf("%s:\n%q\nwant\n%q", c.name, got, c.want)
		}
	}
}

func TestToLegacyDefaultClass(t *testing.T) {
	src := "import {Extension} from 'resource:///org/gnome/shell/extensions/extension.js';\n" +
		"\n" +
		"export default class AIUsagePanelExtension extends Extension {\n" +
		"    enable() {\n" +
		"        this._a = this.dir;\n" +
		"    }\n" +
		"}\n"
	want := "\n" +
		"class AIUsagePanelExtension {\n" +
		"    constructor(meta) {\n" +
		"        this.uuid = meta.uuid;\n" +
		"        this.path = meta.path;\n" +
		"        this.dir = meta.dir;\n" +
		"        this.metadata = meta.metadata;\n" +
		"    }\n" +
		"\n" +
		"    enable() {\n" +
		"        this._a = this.dir;\n" +
		"    }\n" +
		"}\n" +
		"\n" +
		"function init(meta) {\n" +
		"    return new AIUsagePanelExtension(meta);\n" +
		"}\n"
	if got := legacy(t, src); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

// m6: what the GNOME 42 form runs as it is: await in functions, braces
// and the words super and await in strings, comments and template text,
// a class that is not the default one, a "}" in column 0 inside the class.
func TestToLegacyKeepsWhatItCanRun(t *testing.T) {
	src := "import {Extension} from 'resource:///org/gnome/shell/extensions/extension.js';\n" +
		"// super.enable() and await are only words here\n" +
		"const s = '{ super. await';\n" +
		"const t = `${a ? '}' : `{`}`;\n" +
		"/* {\n   super.x() */\n" +
		"class Link {\n    constructor() {\n        this.a = 1;\n    }\n}\n" +
		"async function load() {\n    await later();\n}\n" +
		"const f = async () => {\n    await later();\n};\n" +
		"export default class E extends Extension {\n" +
		"    async enable() {\n" +
		"        if (x) {\n" +
		"            await this._load(); // not super\n" +
		"        }\n" +
		"        items.forEach(async i => {\n            await i.ready;\n        });\n" +
		"        const s = 'super.';\n" +
		"    }\n" +
		"}\n" +
		"\n" +
		"function after() {\n    return 1;\n}\n"
	got := legacy(t, src)
	for _, want := range []string{"class Link {\n    constructor() {", "class E {\n    constructor(meta) {", "function after() {", "function init(meta) {\n    return new E(meta);\n}\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("no %q in\n%s", want, got)
		}
	}
	if strings.Count(got, "constructor(meta)") != 1 {
		t.Fatalf("%s", got)
	}
}

// Every other form with import or export, and a constructor in the
// default class, fail with the file and line (PR-19).
func TestConvertRefusesOtherForms(t *testing.T) {
	for _, c := range []struct {
		src  string
		line int
	}{
		{"const a = 1;\nconst m = import('x');\n", 2},
		{"import X from './x.js';\n", 1},
		{"export default function enable() {}\n", 1},
		{"export default class E extends Extension {\n    constructor(meta) {\n        super(meta);\n    }\n}\n", 2},
		{"export default class E {\n}\n", 1},
		{"export class Panel {}\n", 1},
		{"export let x = 1;\n", 1},
		{"export {a, b};\n", 1},
		{"import {\n    a,\n} from './view.js';\n", 1},
		{"import * as Foo from 'resource:///org/gnome/shell/extensions/other.js';\n", 1},
		{"import Gio from 'gi://GLib';\n", 1},
		{"  import St from 'gi://St';\n", 1},
		{"const u = import.meta.url;\n", 1},
		{"export default class A extends Extension {\n}\nexport default class B extends Extension {\n}\n", 3},
		// m6: what the GNOME 42 form cannot run. The default class has no
		// base class there: no super, no Extension method.
		{"export default class E extends Extension {\n    enable() {\n        super.enable();\n    }\n}\n", 3},
		{"export default class E extends Extension {\n    enable() {\n        this._s = this.getSettings();\n    }\n}\n", 3},
		{"export default class E extends Extension {\n    enable() {\n        this.openPreferences();\n    }\n}\n", 3},
		{"export default class E extends Extension {\n    enable() {\n        this.initTranslations();\n    }\n}\n", 3},
		{"export default class E extends Extension {\n    enable() {\n        this._t = this.gettext('x');\n    }\n}\n", 3},
		{"export default class E extends Extension {\n    enable() {\n        const log = this.getLogger();\n    }\n}\n", 3},
		{"const me = Extension.lookupByURL('x');\n", 1},
		{"class Other extends Extension {\n}\n", 1},
		// the class does not end at an indented "}" or at a "}" in a
		// string: the constructor below is still in it
		{"export default class E extends Extension {\n    enable() {\n        const s = '}';\n}\n    constructor() {\n    }\n}\n", 5},
		{"export default class E extends Extension {\n    enable() {\n        if (a) {\n        }\n  }\n  constructor() {}\n}\n", 6},
		// top-level await: the GNOME 42 form is a script
		{"const a = 1;\nawait start();\n", 2},
		{"if (a) {\n    await start();\n}\n", 2},
		{"for await (const x of xs) {\n}\n", 1},
		// braces or quotes that do not balance
		{"function f() {\n", 1},
		{"const s = 'x;\n", 1},
	} {
		_, err := ToLegacy("extension.js", []byte(c.src))
		want := "extension.js:" + itoa(c.line) + ": not a supported form"
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q: error %v, want %q", c.src, err, want)
		}
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// metadata.json: the same file, its shell-version set for the form.
func TestMetadataForms(t *testing.T) {
	src := []byte("{\n  \"uuid\": \"ai-usage-panel@localhost\",\n  \"name\": \"AI Usage Panel\",\n  \"shell-version\": [\"46\", \"50\"]\n}\n")
	for versions, want := range map[string][]string{"42": {"42"}, "46 50": {"46", "50"}} {
		b, err := Metadata(src, strings.Fields(versions))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: %v", b, err)
		}
		var got []string
		for _, v := range m["shell-version"].([]any) {
			got = append(got, v.(string))
		}
		if !reflect.DeepEqual(got, want) || m["uuid"] != "ai-usage-panel@localhost" || m["name"] != "AI Usage Panel" || len(m) != 3 {
			t.Fatalf("%s", b)
		}
	}
	if b, _ := Metadata(src, []string{"42"}); !strings.Contains(string(b), "\"shell-version\": [\"42\"]\n") {
		t.Fatalf("not one line: %s", b)
	}
	for _, bad := range []string{`{"uuid": "x"}`, "{\n  \"shell-version\": [\"46\"],\n  \"shell-version\": [\"50\"]\n}\n", "{\n  \"shell-version\": [\"46\"\n  ]\n}\n"} {
		if _, err := Metadata([]byte(bad), []string{"42"}); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestBuild(t *testing.T) {
	src := map[string][]byte{
		"metadata.json":  []byte("{\n  \"uuid\": \"ai-usage-panel@localhost\",\n  \"shell-version\": [\"46\", \"50\"]\n}\n"),
		"extension.js":   []byte("import St from 'gi://St';\nexport default class E extends Extension {\n}\n"),
		"view.js":        []byte("export const PROTOCOL = 1;\n"),
		"stylesheet.css": []byte(".a { color: red; }\n"),
		"clawd.svg":      []byte("<svg/>\n"),
	}
	if len(Forms) != 2 || Forms[0].Name != "gnome42" || Forms[1].Name != "gnome46" {
		t.Fatalf("%+v", Forms)
	}
	f42, err := Build(Forms[0], src)
	if err != nil {
		t.Fatal(err)
	}
	f46, err := Build(Forms[1], src)
	if err != nil {
		t.Fatal(err)
	}
	if len(f42) != 5 || len(f46) != 5 {
		t.Fatalf("%d, %d files", len(f42), len(f46))
	}
	for _, name := range []string{"stylesheet.css", "clawd.svg"} {
		if string(f42[name]) != string(src[name]) || string(f46[name]) != string(src[name]) {
			t.Fatalf("%s not copied", name)
		}
	}
	for _, name := range []string{"extension.js", "view.js"} {
		if string(f46[name]) != string(src[name]) {
			t.Fatalf("the ES module %s is not a copy", name)
		}
	}
	if string(f42["view.js"]) != "'use strict';\nvar PROTOCOL = 1;\n" || !strings.Contains(string(f42["extension.js"]), "function init(meta)") {
		t.Fatalf("not converted: %q %q", f42["view.js"], f42["extension.js"])
	}
	if !strings.Contains(string(f42["metadata.json"]), `["42"]`) || !strings.Contains(string(f46["metadata.json"]), `["46", "50"]`) {
		t.Fatalf("%s %s", f42["metadata.json"], f46["metadata.json"])
	}
	src["view.js"] = []byte("export default function f() {}\n")
	if _, err := Build(Forms[0], src); err == nil || !strings.Contains(err.Error(), "view.js:1:") {
		t.Fatalf("%v", err)
	}
	delete(src, "view.js")
	delete(src, "metadata.json")
	if _, err := Build(Forms[1], src); err == nil {
		t.Fatal("a source without metadata.json")
	}
}
