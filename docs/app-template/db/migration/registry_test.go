package migration

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"testing"
)

// TestAllIsComplete fails when a migration file exists but All() does not list it,
// or when two migrations share a Name(): the migrate command would silently skip
// the unlisted one, and would treat the second of a pair as already applied.
func TestAllIsComplete(t *testing.T) {
	listed := map[string]bool{}
	names := map[string]bool{}
	for _, m := range All() {
		listed[reflect.TypeOf(m).Name()] = true
		if names[m.Name()] {
			t.Errorf("migration name %q is used twice", m.Name())
		}
		names[m.Name()] = true
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Name.Name != "Up" {
				continue
			}
			receiver := fn.Recv.List[0].Type
			if star, ok := receiver.(*ast.StarExpr); ok {
				receiver = star.X
			}
			if ident, ok := receiver.(*ast.Ident); ok && !listed[ident.Name] {
				t.Errorf("%s (%s) has an Up() but is not in All()", ident.Name, entry.Name())
			}
		}
	}
}
