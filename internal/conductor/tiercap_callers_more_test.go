package conductor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// placementSensitivityCallerFiles are the test files whose router
// constructions must each pass the fixture LaneTierCap.
var placementSensitivityCallerFiles = []string{"router_placement_test.go", "sensitivity_test.go"}

// TestPlacementSensitivityCallersPassTierCap parses the caller files and
// fails on any NewRouter or NewDaemonRouter construction that does not pass
// WithLaneTierCap(conductorTestCap): NewDaemonRouter takes it as an option
// argument; a NewRouter statement must be followed directly by
// WithLaneTierCap(conductorTestCap)(<router>).
func TestPlacementSensitivityCallersPassTierCap(t *testing.T) {
	for _, name := range placementSensitivityCallerFiles {
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		found, missing := 0, 0
		ast.Inspect(f, func(n ast.Node) bool {
			block, ok := n.(*ast.BlockStmt)
			if !ok {
				return true
			}
			for i, stmt := range block.List {
				for _, call := range routerConstructions(stmt) {
					found++
					if !constructionCapped(call, block.List, i) {
						missing++
						t.Errorf("%s: %s without WithLaneTierCap(conductorTestCap)", name, calleeName(call))
					}
				}
			}
			return true
		})
		if found == 0 {
			t.Errorf("%s: no router construction found; the gate is vacuous", name)
		}
		t.Logf("%s: %d router constructions, %d missing the tier cap", name, found, missing)
	}
}

// routerConstructions returns the NewRouter and NewDaemonRouter calls in
// stmt, not descending into function literals (their bodies are separate
// blocks the walk visits on its own).
func routerConstructions(stmt ast.Stmt) []*ast.CallExpr {
	var calls []*ast.CallExpr
	ast.Inspect(stmt, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			if c := calleeName(v); c == "NewRouter" || c == "NewDaemonRouter" {
				calls = append(calls, v)
			}
		}
		return true
	})
	return calls
}

// constructionCapped reports whether call (found in list[i]) passes the
// fixture cap: as an argument for NewDaemonRouter, or as the very next
// statement for NewRouter.
func constructionCapped(call *ast.CallExpr, list []ast.Stmt, i int) bool {
	if calleeName(call) == "NewDaemonRouter" {
		for _, arg := range call.Args {
			if isFixtureCapOption(arg) {
				return true
			}
		}
		return false
	}
	if i+1 >= len(list) {
		return false
	}
	es, ok := list[i+1].(*ast.ExprStmt)
	if !ok {
		return false
	}
	apply, ok := es.X.(*ast.CallExpr)
	return ok && len(apply.Args) == 1 && isFixtureCapOption(apply.Fun)
}

// isFixtureCapOption reports whether e is WithLaneTierCap(conductorTestCap).
func isFixtureCapOption(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok || calleeName(call) != "WithLaneTierCap" || len(call.Args) != 1 {
		return false
	}
	id, ok := call.Args[0].(*ast.Ident)
	return ok && id.Name == "conductorTestCap"
}

// calleeName is the bare identifier a call invokes, or "" for anything else.
func calleeName(call *ast.CallExpr) string {
	if id, ok := call.Fun.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}
