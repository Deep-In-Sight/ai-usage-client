package gnomeext

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Deep-In-Sight/ai-usage-client/agent/tools/mkext/convert"
)

// The extension's CSS class names and asset files (what its JavaScript
// reads from the folder at run time) that this build ships. These lists
// only grow, and a name in them keeps its meaning (review A-m2, the
// version-mix rule of design 6.4): after an update the running shell keeps
// the JavaScript it loaded at login, but reads stylesheet.css (and
// clawd.svg) from the swapped folder again at its next enable (a lock and
// unlock, the switch in the Extensions app), so until the next login the
// old JavaScript draws with the new stylesheet and Clawd. A later build
// adds names to these lists; it never removes or renames one.
var (
	shippedClasses = []string{
		"ai-usage-critical",
		"ai-usage-fill",
		"ai-usage-fill-critical",
		"ai-usage-fill-normal",
		"ai-usage-fill-warning",
		"ai-usage-header",
		"ai-usage-info",
		"ai-usage-label",
		"ai-usage-name",
		"ai-usage-on-light",
		"ai-usage-percent",
		"ai-usage-pill",
		"ai-usage-problem",
		"ai-usage-row",
		"ai-usage-section",
		"ai-usage-small",
		"ai-usage-top",
		"ai-usage-track",
	}
	shippedAssets = []string{"clawd.svg", "stylesheet.css"}
)

var cssClass = regexp.MustCompile(`\.(ai-usage-[a-z0-9-]+)`)

// namesOf checks one folder's files (the source's, or an embedded form's)
// against the lists: every listed name is there, and every class in the
// stylesheet and every file but the code (*.js, metadata.json) is listed,
// so that a new one joins the lists and is kept from then on.
func namesOf(t *testing.T, where string, files map[string][]byte) {
	t.Helper()
	css, ok := files["stylesheet.css"]
	if !ok {
		t.Fatalf("%s: no stylesheet.css", where)
	}
	var classes []string
	for _, m := range cssClass.FindAllStringSubmatch(string(css), -1) {
		if !slices.Contains(classes, m[1]) {
			classes = append(classes, m[1])
		}
	}
	for _, c := range shippedClasses {
		if !slices.Contains(classes, c) {
			t.Errorf("%s: stylesheet.css lost the class %s (names only grow: the running shell's old JavaScript still uses it)", where, c)
		}
	}
	for _, c := range classes {
		if !slices.Contains(shippedClasses, c) {
			t.Errorf("%s: the class %s is not in shippedClasses (add it: from now on it is kept)", where, c)
		}
	}
	for _, a := range shippedAssets {
		if _, ok := files[a]; !ok {
			t.Errorf("%s: the asset %s is gone (names only grow: the running shell's old JavaScript still reads it)", where, a)
		}
	}
	for name := range files {
		if !strings.HasSuffix(name, ".js") && name != "metadata.json" && !slices.Contains(shippedAssets, name) {
			t.Errorf("%s: the file %s is not in shippedAssets (add it: from now on it is kept)", where, name)
		}
	}
}

// Review A-m2: the source and every embedded form ship at least the
// names of the lists, and nothing outside them.
func TestExtensionNamesOnlyGrow(t *testing.T) {
	if len(shippedClasses) < 18 || len(shippedAssets) < 2 {
		t.Fatalf("the lists only grow: %d classes, %d assets", len(shippedClasses), len(shippedAssets))
	}
	srcDir := filepath.Join("..", "..", "..", "gnome-extension", UUID)
	entries, err := os.ReadDir(srcDir)
	must(t, err)
	src := map[string][]byte{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(srcDir, e.Name()))
		must(t, err)
		src[e.Name()] = b
	}
	namesOf(t, "the source", src)
	needForms(t)
	for _, f := range convert.Forms {
		files, err := formFiles(f.Name)
		must(t, err)
		namesOf(t, f.Name, files)
	}
}
