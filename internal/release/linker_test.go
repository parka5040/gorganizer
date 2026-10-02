package release

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestProductionReleaseHasNoLinkerStrings checks no production package variable is a string that -X could replace.
func TestProductionReleaseHasNoLinkerStrings(t *testing.T) {
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_fixture.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				values := spec.(*ast.ValueSpec)
				if ident, ok := values.Type.(*ast.Ident); ok && ident.Name == "string" {
					t.Errorf("%s declares string variable %s", name, values.Names[0].Name)
				}
				for _, value := range values.Values {
					if literal, ok := value.(*ast.BasicLit); ok && literal.Kind == token.STRING {
						t.Errorf("%s initializes variable %s from a string literal", name, values.Names[0].Name)
					}
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no production release files were checked")
	}
}
