package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// acceptDialViolations reports, for one parsed file, every call that passes
// contextFactsAcceptMethod to anything but callElevated, and whether any
// callElevated call carries it.
func acceptDialViolations(f *ast.File) (bad []string, viaCallElevated int) {
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		passes := false
		for _, a := range call.Args {
			if id, ok := a.(*ast.Ident); ok && id.Name == "contextFactsAcceptMethod" {
				passes = true
			}
		}
		if !passes {
			return true
		}
		name := ""
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			name = fn.Name
		case *ast.SelectorExpr:
			name = fn.Sel.Name
		}
		switch name {
		case "callElevated":
			viaCallElevated++
		case "RegisterElevatedVerb", "DeclareAccess", "Register":
		default:
			bad = append(bad, name)
		}
		return true
	})
	return bad, viaCallElevated
}

func TestFactsAcceptCommandUsesCallElevated(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "context_facts.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	bad, via := acceptDialViolations(f)
	if len(bad) != 0 || via != 1 {
		t.Fatalf("context_facts.go dials %s via %v and %d callElevated calls, want exactly one callElevated", contextFactsAcceptMethod, bad, via)
	}
	seed, err := parser.ParseFile(token.NewFileSet(), "seed.go",
		"package main\nfunc s() { _ = c.Do(ctx, contextFactsAcceptMethod, p, nil); _ = c.DoElevated(ctx, contextFactsAcceptMethod, p, nil, nil) }\n", 0)
	if err != nil {
		t.Fatal(err)
	}
	if bad, via := acceptDialViolations(seed); len(bad) != 2 || via != 0 {
		t.Fatalf("seeded direct dials not caught: %v, %d", bad, via)
	}
}
