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
	// calls reports whether a method with receiver recv calls name on the
	// same receiver (d.skipOptionalLeg, not any selector that happens to
	// share the name).
	calls := func(file, recv, name string) bool {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(".", file), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		recvOf := func(fn *ast.FuncDecl) string {
			if fn.Recv == nil || len(fn.Recv.List) == 0 {
				return ""
			}
			switch e := fn.Recv.List[0].Type.(type) {
			case *ast.StarExpr:
				if id, ok := e.X.(*ast.Ident); ok {
					return id.Name
				}
			case *ast.Ident:
				return e.Name
			}
			return ""
		}
		found := false
		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || recvOf(fn) != recv {
				return true
			}
			hit := false
			ast.Inspect(fn.Body, func(m ast.Node) bool {
				call, ok := m.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != name {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == fn.Recv.List[0].Names[0].Name {
					hit = true
					return false
				}
				return true
			})
			found = found || hit
			return false
		})
		return found
	}
	for _, c := range []struct{ file, recv string }{
		{"driver.go", "Driver"},
		{"remote.go", "RemoteDriver"},
	} {
		if !calls(c.file, c.recv, "skipOptionalLeg") {
			t.Errorf("%s: no %s.skipOptionalLeg call (an optional leg's start must gate on its skip check)", c.file, c.recv)
		}
	}
	// usesNeeds reports whether file ranges over the optional leg's Needs()
	// in the AST: a range statement (or any call) whose function is a
	// Needs selector. Comments and string literals cannot satisfy it.
	usesNeeds := func(file string) bool {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(".", file), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Needs" {
				return true
			}
			found = true
			return false
		})
		return found
	}
	for _, f := range []string{"driver_optional.go", "remote_optional.go"} {
		if !usesNeeds(f) {
			t.Errorf("%s: no Needs() call (the check must read every need at its count, not only the primary item)", f)
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
