package creds_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/creds"
)

const module = "github.com/Deep-In-Sight/ai-usage-client/agent"

// agentRoot is the agent/ folder (this test runs in agent/internal/creds).
func agentRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("agent root not found: %v", err)
	}
	return root
}

type goFile struct {
	rel  string // slash-separated, relative to agent/
	file *ast.File
}

// sources parses every non-test .go file under agent/.
func sources(t *testing.T) []goFile {
	t.Helper()
	root := agentRoot(t)
	var out []goFile
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == "testdata" || n == "dist" || strings.HasPrefix(n, ".") && p != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, goFile{rel: filepath.ToSlash(rel), file: f})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) < 10 {
		t.Fatalf("only %d source files found", len(out))
	}
	return out
}

func TestOnlyAnthropicCallsWithToken(t *testing.T) {
	calls := 0
	for _, f := range sources(t) {
		ast.Inspect(f.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "WithToken" {
				return true
			}
			calls++
			// Claude's token reaches the request in internal/anthropic,
			// Codex's in internal/codex (plan Global Constraint 1)
			if !strings.HasPrefix(f.rel, "internal/anthropic/") && !strings.HasPrefix(f.rel, "internal/codex/") {
				t.Errorf("%s calls WithToken; only internal/anthropic and internal/codex may", f.rel)
			}
			return true
		})
	}
	if calls == 0 {
		t.Fatal("no WithToken call found at all; the check is not looking at the right files")
	}
}

func TestServerSideNeverImportsCredsOrAnthropic(t *testing.T) {
	guarded := []string{"internal/sync/protocol/", "internal/sync/discover/", "internal/sync/backlog/", "internal/status/",
		// the tray side: packages that do not exist yet are simply not found
		"internal/tray/", "internal/notify/", "internal/icon/", "internal/autostart/", "internal/winapp/", "internal/procs/"}
	banned := []string{module + "/internal/creds", module + "/internal/anthropic", module + "/internal/codex"}
	for _, f := range sources(t) {
		in := false
		for _, g := range guarded {
			in = in || strings.HasPrefix(f.rel, g)
		}
		if !in {
			continue
		}
		for _, imp := range f.file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			for _, b := range banned {
				if p == b || strings.HasPrefix(p, b+"/") {
					t.Errorf("%s imports %s", f.rel, p)
				}
			}
		}
	}
}

func TestLoginHasNoTokenField(t *testing.T) {
	typ := reflect.TypeOf(creds.Login{})
	for i := 0; i < typ.NumField(); i++ {
		n := typ.Field(i).Name
		if strings.Contains(strings.ToLower(n), "token") && n != "HasToken" {
			t.Errorf("creds.Login has field %s", n)
		}
	}
	typ = reflect.TypeOf(creds.Account{})
	for i := 0; i < typ.NumField(); i++ {
		if strings.Contains(strings.ToLower(typ.Field(i).Name), "token") {
			t.Errorf("creds.Account has field %s", typ.Field(i).Name)
		}
	}
}
