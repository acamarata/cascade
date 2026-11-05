package review

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: TestReviewRouterCallersPassTierCap (P1-TOP-02j) walks every
//   _test.go file of internal/review with go/ast and fails on any router
//   construction (conductor.NewRouter or conductor.NewDaemonRouter) whose
//   enclosing function does not pass WithLaneTierCap(reviewTestCap).
// SPORT: internal/review.tiercap-callers (ADD, P1-TOP-02j).

// routerConstructors names the conductor router constructors the gate checks.
var routerConstructors = map[string]bool{"NewRouter": true, "NewDaemonRouter": true}

// isConductorCall reports whether n is a call to conductor.<name> for a name
// accepted by want.
func isConductorCall(n ast.Node, want func(string) bool) (*ast.CallExpr, bool) {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil, false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "conductor" || !want(sel.Sel.Name) {
		return nil, false
	}
	return call, true
}

// passesTierCap reports whether body holds conductor.WithLaneTierCap(reviewTestCap).
func passesTierCap(body ast.Node) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := isConductorCall(n, func(s string) bool { return s == "WithLaneTierCap" })
		if ok && len(call.Args) == 1 {
			if id, ok := call.Args[0].(*ast.Ident); ok && id.Name == "reviewTestCap" {
				found = true
			}
		}
		return !found
	})
	return found
}

// routerCallersMissingCap returns "file:line" for every router construction
// in f whose enclosing function lacks WithLaneTierCap(reviewTestCap), and the
// count of constructions seen.
func routerCallersMissingCap(fset *token.FileSet, f *ast.File) (missing []string, seen int) {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		capped := passesTierCap(fn.Body)
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if _, ok := isConductorCall(n, func(s string) bool { return routerConstructors[s] }); ok {
				seen++
				if !capped {
					missing = append(missing, fset.Position(n.Pos()).String())
				}
			}
			return true
		})
	}
	return missing, seen
}

func TestReviewRouterCallersPassTierCap(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	total := 0
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		missing, seen := routerCallersMissingCap(fset, f)
		total += seen
		if len(missing) > 0 {
			t.Errorf("router construction without WithLaneTierCap(reviewTestCap): %s", strings.Join(missing, ", "))
		}
	}
	if total == 0 {
		t.Fatal("no router construction found in internal/review test files; the gate would pass vacuously")
	}
}

func TestReviewRouterCallersPassTierCapSeededRed(t *testing.T) {
	src := `package review
func bad() { _ = conductor.NewRouter(nil, nil, nil, nil) }
func good() { r := conductor.NewRouter(nil, nil, nil, nil); conductor.WithLaneTierCap(reviewTestCap)(r) }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "seed_test.go", src, 0)
	if err != nil {
		t.Fatalf("parse seed: %v", err)
	}
	missing, seen := routerCallersMissingCap(fset, f)
	if seen != 2 || len(missing) != 1 || !strings.HasPrefix(missing[0], "seed_test.go:2:") {
		t.Fatalf("seeded gate: seen=%d missing=%v, want 2 seen and only line 2 missing", seen, missing)
	}
}
