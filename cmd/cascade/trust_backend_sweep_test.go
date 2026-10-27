package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// trustSinks are the elevation calls a trust backend may be handed to:
// BoundTrust (the only reader that decides elevation) and the enrolment
// writers, which create the record BoundTrust later checks.
var trustSinks = map[string]bool{"BoundTrust": true, "EnrollPresence": true, "ReplaceFileTierEnrollment": true}

// trustSweep reports every place a non-test file builds an elevation trust
// backend or trust store other than as an argument of a trust sink. A
// function (or func-valued field) whose body returns NewFileBackend is a
// factory: each call of it is held to the same rule. NewElevationTrustStore
// is allowed only as an enrolment writer: its result is used for Enroll
// and nothing else.
func trustSweep(fset *token.FileSet, f *ast.File, factories map[string]bool) (violations []string, routed int) {
	isElev := elevCall(f)
	parent, factoryReturns := callIndex(f)
	calledName := func(c *ast.CallExpr) string {
		switch fn := c.Fun.(type) {
		case *ast.Ident:
			return fn.Name
		case *ast.SelectorExpr:
			return fn.Sel.Name
		}
		return ""
	}
	sunk := func(c *ast.CallExpr) bool {
		p, ok := parent[c]
		if !ok {
			return false
		}
		for name := range trustSinks {
			if isElev(p.Fun, name) {
				return true
			}
		}
		return isElev(p.Fun, "NewElevationTrustStore") && enrollOnly(f, p)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		construct := isElev(c.Fun, "NewFileBackend") || factories[calledName(c)]
		switch {
		case isElev(c.Fun, "NewElevationTrustStore") && !enrollOnly(f, c):
			violations = append(violations, fset.Position(c.Pos()).String()+" NewElevationTrustStore used beyond Enroll")
		case construct && isElev(c.Fun, "NewFileBackend") && factoryReturns[c]:
			// the factory's own return; its callers are checked instead
		case construct && sunk(c):
			routed++
		case construct:
			violations = append(violations, fset.Position(c.Pos()).String()+" trust backend built outside BoundTrust")
		}
		return true
	})
	return violations, routed
}

// callIndex maps each call passed directly as an argument to the call that
// receives it, and marks every call that is returned as-is.
func callIndex(f *ast.File) (parent map[*ast.CallExpr]*ast.CallExpr, returned map[*ast.CallExpr]bool) {
	parent, returned = map[*ast.CallExpr]*ast.CallExpr{}, map[*ast.CallExpr]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			for _, arg := range x.Args {
				if c, ok := arg.(*ast.CallExpr); ok {
					parent[c] = x
				}
			}
		case *ast.ReturnStmt:
			for _, r := range x.Results {
				if c, ok := r.(*ast.CallExpr); ok {
					returned[c] = true
				}
			}
		}
		return true
	})
	return parent, returned
}

// elevCall matches a selector on the file's internal/elevation import.
func elevCall(f *ast.File) func(ast.Expr, string) bool {
	aliases := elevationAliases(f)
	return func(e ast.Expr, name string) bool {
		sel, ok := e.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != name {
			return false
		}
		id, ok := sel.X.(*ast.Ident)
		return ok && aliases[id.Name]
	}
}

// collectFactories adds the name of every function, and every func-valued
// composite-literal field, whose body returns NewFileBackend.
func collectFactories(f *ast.File, factories map[string]bool) {
	isElev := elevCall(f)
	returnsBackend := func(body *ast.BlockStmt) bool {
		found := false
		ast.Inspect(body, func(n ast.Node) bool {
			if _, nested := n.(*ast.FuncLit); nested {
				return false // a nested literal's return is its own, not this body's
			}
			if r, ok := n.(*ast.ReturnStmt); ok && len(r.Results) == 1 {
				if c, ok := r.Results[0].(*ast.CallExpr); ok && isElev(c.Fun, "NewFileBackend") {
					found = true
				}
			}
			return !found
		})
		return found
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			if x.Body != nil && returnsBackend(x.Body) {
				factories[x.Name.Name] = true
			}
		case *ast.KeyValueExpr:
			if key, ok := x.Key.(*ast.Ident); ok {
				if lit, ok := x.Value.(*ast.FuncLit); ok && returnsBackend(lit.Body) {
					factories[key.Name] = true
				}
			}
		}
		return true
	})
}

// enrollOnly reports whether the trust store built by call is used only to
// Enroll: chained directly, or assigned to a name whose every use is Enroll.
func enrollOnly(f *ast.File, call *ast.CallExpr) bool {
	var scope ast.Node = f
	ast.Inspect(f, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.FuncDecl, *ast.FuncLit:
			if n.Pos() <= call.Pos() && call.End() <= n.End() {
				scope = n
			}
		}
		return true
	})
	var name string
	chained := false
	ast.Inspect(scope, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if x.X == call && x.Sel.Name == "Enroll" {
				chained = true
			}
		case *ast.AssignStmt:
			if len(x.Rhs) == 1 && x.Rhs[0] == call && len(x.Lhs) == 1 {
				if id, ok := x.Lhs[0].(*ast.Ident); ok {
					name = id.Name
				}
			}
		}
		return true
	})
	if chained {
		return true
	}
	if name == "" || name == "_" {
		return false
	}
	uses, enrolls := 0, 0
	ast.Inspect(scope, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == name && sel.Sel.Name == "Enroll" {
				enrolls++
			}
		}
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			uses++
		}
		return true
	})
	return enrolls > 0 && uses == enrolls+1
}

func TestProductionTrustStoresAreBound(t *testing.T) {
	root := filepath.Join("..", "..")
	type parsed struct {
		fset *token.FileSet
		file *ast.File
	}
	var all []parsed
	for _, dir := range []string{"cmd", "internal", "plugins", "providers"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
				strings.HasPrefix(filepath.ToSlash(path), filepath.ToSlash(filepath.Join(root, "internal", "elevation"))+"/") {
				return err
			}
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return perr
			}
			all = append(all, parsed{fset, f})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	factories := map[string]bool{}
	for _, p := range all {
		collectFactories(p.file, factories)
	}
	files, routed := len(all), 0
	var violations []string
	for _, p := range all {
		v, r := trustSweep(p.fset, p.file, factories)
		violations, routed = append(violations, v...), routed+r
	}
	// The walk must have seen the tree and the known BoundTrust routes, or
	// a zero-violation result proves nothing.
	if files < 100 || routed < 5 || len(violations) > 0 {
		t.Fatalf("scanned=%d routed=%d violations=%v", files, routed, violations)
	}
	assertSeededTrustSweep(t)
}

// assertSeededTrustSweep proves the sweep captures each seeded direct
// construction and passes the two allowed shapes.
func assertSeededTrustSweep(t *testing.T) {
	t.Helper()
	seeded := map[string]string{
		"direct":       "package p\nimport e \"github.com/acamarata/cascade/internal/elevation\"\nfunc f(d string) { b := e.NewFileBackend(d); _ = b }\n",
		"isenrolled":   "package p\nimport \"github.com/acamarata/cascade/internal/elevation\"\nfunc f(d string) bool { return elevation.NewElevationTrustStore(elevation.BoundTrustless(d), nil).IsEnrolled() }\n",
		"factory":      "package p\nimport \"github.com/acamarata/cascade/internal/elevation\"\nfunc mk(d string) elevation.Backend { return elevation.NewFileBackend(d) }\nfunc g(d string) { _ = mk(d) }\n",
		"field":        "package p\nimport \"github.com/acamarata/cascade/internal/elevation\"\nvar x = s{TB: func() elevation.Backend { return elevation.NewFileBackend(\"d\") }}\nfunc g() { h(x.TB()) }\n",
		"bound":        "package p\nimport \"github.com/acamarata/cascade/internal/elevation\"\nfunc f(c elevation.Custody, d string) { elevation.BoundTrust(c, elevation.NewFileBackend(d), nil) }\n",
		"enroll-write": "package p\nimport \"github.com/acamarata/cascade/internal/elevation\"\nfunc f(d, k string) { s := elevation.NewElevationTrustStore(elevation.NewFileBackend(d), nil); s.Enroll(k) }\n",
	}
	want := map[string]int{"direct": 1, "isenrolled": 1, "factory": 1, "field": 1, "bound": 0, "enroll-write": 0}
	for label, src := range seeded {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, label+".go", src, 0)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		factories := map[string]bool{}
		collectFactories(f, factories)
		if v, _ := trustSweep(fset, f, factories); len(v) != want[label] {
			t.Fatalf("seeded %s: violations %v, want %d", label, v, want[label])
		}
	}
}
