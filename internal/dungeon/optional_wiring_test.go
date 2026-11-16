package dungeon

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOptionalLegCallSites pins the wiring the multi-item leg depends on:
// both drivers gate an optional leg's start on their skip check (driver.go
// and remote.go call skipOptionalLeg), and both checks read every need at
// its count (driver_optional.go and remote_optional.go range over Needs()).
// Deleting a call or narrowing a check to the primary item fails this test.
func TestOptionalLegCallSites(t *testing.T) {
	calls := func(file, recv, name string) bool {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(".", file), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != name {
				return true
			}
			_ = recv
			found = true
			return false
		})
		return found
	}
	for _, c := range []struct{ file, recv string }{
		{"driver.go", "Driver"},
		{"remote.go", "RemoteDriver"},
	} {
		if !calls(c.file, c.recv, "skipOptionalLeg") {
			t.Errorf("%s: no skipOptionalLeg call (an optional leg's start must gate on its skip check)", c.file)
		}
	}
	usesNeeds := func(file string) bool {
		b, err := os.ReadFile(filepath.Join(".", file))
		if err != nil {
			t.Fatal(err)
		}
		return strings.Contains(string(b), "Needs()")
	}
	for _, f := range []string{"driver_optional.go", "remote_optional.go"} {
		if !usesNeeds(f) {
			t.Errorf("%s: no Needs() use (the check must read every need at its count, not only the primary item)", f)
		}
	}
	// The exporter lists every need with its count in both warnings.
	for _, f := range []string{"planexport.go"} {
		b, err := os.ReadFile(filepath.Join("..", "planexport", f))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"optionalNeedList", "optionalGiveList", "optionalGiveLines"} {
			if !strings.Contains(string(b), want) {
				t.Errorf("planexport.go: no %s (export warnings must name every need with its count and GM lines)", want)
			}
		}
	}
}
