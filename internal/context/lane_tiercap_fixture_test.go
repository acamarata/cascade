package context

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: the context package's test-only lane tier cap (P1-TOP-02h) and
//   TestRouterCallersPassTierCap, a go/ast gate over the _test.go files of
//   internal/context, internal/conversation and internal/review that fails
//   on any router construction (conductor.NewRouter or
//   conductor.NewDaemonRouter) whose enclosing function does not pass
//   WithLaneTierCap(<pkg>TestCap).
// Constraints: the cap is admit-all at local-only, the most restrictive
//   rank, so it never narrows what the sensitivity filter already decides.
// SPORT: internal/context.lane-tiercap-fixture (ADD, P1-TOP-02h).

// contextTestCap is the test-only admit-all cap: every lane may carry
// local-only content.
func contextTestCap(context.Context, conductor.LaneID, conductor.LaneType) (provider.SensitivityTier, string, error) {
	return provider.SensitivityLocalOnly, "test-fixture", nil
}

// tierCapGatePackages maps each gated package directory, relative to this
// one, to the cap identifier its router callers must pass.
var tierCapGatePackages = map[string]string{
	".":               "contextTestCap",
	"../conversation": "conversationTestCap",
	"../review":       "reviewTestCap",
}

// tierCapGateConductorCall reports whether n is a call to conductor.<name>
// for a name accepted by want.
func tierCapGateConductorCall(n ast.Node, want func(string) bool) (*ast.CallExpr, bool) {
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

// tierCapGatePasses reports whether body holds conductor.WithLaneTierCap(capName).
func tierCapGatePasses(body ast.Node, capName string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := tierCapGateConductorCall(n, func(s string) bool { return s == "WithLaneTierCap" })
		if ok && len(call.Args) == 1 {
			if id, ok := call.Args[0].(*ast.Ident); ok && id.Name == capName {
				found = true
			}
		}
		return !found
	})
	return found
}

// tierCapGateMissing returns "file:line" for every router construction in f
// whose enclosing function lacks WithLaneTierCap(capName), and the count of
// constructions seen.
func tierCapGateMissing(fset *token.FileSet, f *ast.File, capName string) (missing []string, seen int) {
	isRouter := func(s string) bool { return s == "NewRouter" || s == "NewDaemonRouter" }
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		capped := tierCapGatePasses(fn.Body, capName)
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if _, ok := tierCapGateConductorCall(n, isRouter); ok {
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

func TestRouterCallersPassTierCap(t *testing.T) {
	t.Run("seeded-red", func(t *testing.T) {
		src := `package context
func bad() { _ = conductor.NewDaemonRouter(nil, nil, nil, nil, nil) }
func wrong() { r := conductor.NewRouter(nil, nil, nil, nil); conductor.WithLaneTierCap(reviewTestCap)(r) }
func good() { r := conductor.NewRouter(nil, nil, nil, nil); conductor.WithLaneTierCap(contextTestCap)(r) }
`
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "seed_test.go", src, 0)
		if err != nil {
			t.Fatalf("parse seed: %v", err)
		}
		missing, seen := tierCapGateMissing(fset, f, "contextTestCap")
		if seen != 3 || len(missing) != 2 ||
			!strings.HasPrefix(missing[0], "seed_test.go:2:") || !strings.HasPrefix(missing[1], "seed_test.go:3:") {
			t.Fatalf("seeded gate: seen=%d missing=%v, want 3 seen and lines 2 and 3 missing", seen, missing)
		}
	})
	for dir, capName := range tierCapGatePackages {
		files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
		if err != nil {
			t.Fatalf("glob %s: %v", dir, err)
		}
		fset := token.NewFileSet()
		total := 0
		for _, name := range files {
			f, err := parser.ParseFile(fset, name, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}
			missing, seen := tierCapGateMissing(fset, f, capName)
			total += seen
			if len(missing) > 0 {
				t.Errorf("router construction without WithLaneTierCap(%s): %s", capName, strings.Join(missing, ", "))
			}
		}
		if total == 0 {
			t.Errorf("no router construction found in %s test files; the gate would pass vacuously", dir)
		}
	}
}
