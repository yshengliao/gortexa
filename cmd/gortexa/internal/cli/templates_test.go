package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestLogicTemplateSymbolsAreEntityNamespaced guards against the collision that
// broke `gortexa gen` on a fresh scaffold: the logic template must not declare
// any package-level identifier that a second generated service (or the sample
// internal/logic/resource.go) would redeclare in package logic. Every top-level
// name it introduces must therefore be namespaced by the entity; anything else
// (like a bare `const defaultPageSize`) collides.
func TestLogicTemplateSymbolsAreEntityNamespaced(t *testing.T) {
	d := tmplData{
		Module: "example.com/app", Domain: "shop", Version: "v1", GoPkg: "shopv1",
		Entity: "Product", Snake: "product", Plural: "Products", PluralSnake: "products",
	}
	src, err := renderTemplate("logic.tmpl", d)
	if err != nil {
		t.Fatalf("render logic.tmpl: %v", err)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "product.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse rendered logic: %v\n%s", err, src)
	}

	for _, decl := range file.Decls {
		switch dd := decl.(type) {
		case *ast.FuncDecl:
			// Methods are scoped to the receiver type, so they never collide.
			if dd.Recv != nil {
				continue
			}
			if !strings.Contains(dd.Name.Name, d.Entity) {
				t.Errorf("package-level func %q is not entity-namespaced; a second generated service would redeclare it", dd.Name.Name)
			}
		case *ast.GenDecl:
			if dd.Tok != token.CONST && dd.Tok != token.VAR && dd.Tok != token.TYPE {
				continue
			}
			for _, spec := range dd.Specs {
				switch s := spec.(type) {
				case *ast.ValueSpec:
					for _, name := range s.Names {
						if name.Name != "_" && !strings.Contains(name.Name, d.Entity) {
							t.Errorf("package-level %s %q is not entity-namespaced; a second generated service would redeclare it", dd.Tok, name.Name)
						}
					}
				case *ast.TypeSpec:
					if !strings.Contains(s.Name.Name, d.Entity) {
						t.Errorf("package-level type %q is not entity-namespaced; a second generated service would redeclare it", s.Name.Name)
					}
				}
			}
		}
	}
}
