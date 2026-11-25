package convert

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func legacy(t *testing.T, src string) string {
	t.Helper()
	b, err := ToLegacy("extension.js", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
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
	if string(f42["view.js"]) != "var PROTOCOL = 1;\n" || !strings.Contains(string(f42["extension.js"]), "function init(meta)") {
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
