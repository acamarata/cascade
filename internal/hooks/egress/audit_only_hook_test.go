package egress

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

func auditHookProof(t *testing.T, root string) bool {
	t.Helper()
	dispatcher, err := os.ReadFile(filepath.Join(root, "internal", "hooks", "dispatcher.go"))
	if err != nil {
		t.Fatal(err)
	}
	params, err := os.ReadFile(filepath.Join(root, "internal", "hooks", "egress_params.go"))
	if err != nil {
		t.Fatal(err)
	}
	guard := "cfg.EgressToken.Class() != egress.EgressClassHook"
	if !bytes.Contains(dispatcher, []byte(guard)) {
		t.Error("production guard missing")
	}
	deleted := bytes.ReplaceAll(dispatcher, []byte(guard), []byte("false"))
	if auditHookGuard(parseAuditSource(t, deleted)) {
		t.Error("deleted dispatcher guard still accepted")
	}
	if auditHookIntercept(parseAuditSource(t, bytes.ReplaceAll(params, []byte("Intercept(ctx, token,"), []byte("Intercept(ctx, other,")))) {
		t.Error("replaced token still accepted")
	}
	return auditHookGuard(parseAuditSource(t, dispatcher)) && auditHookIntercept(parseAuditSource(t, params))
}

func parseAuditSource(t *testing.T, src []byte) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "hooks.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func auditNodeText(n ast.Node) string {
	var out bytes.Buffer
	if err := format.Node(&out, token.NewFileSet(), n); err != nil {
		return ""
	}
	return out.String()
}

func auditHookGuard(f *ast.File) bool {
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "validateDispatcherConfig" || fn.Body == nil {
			continue
		}
		guard, refuse := false, false
		for _, stmt := range fn.Body.List {
			assign, ok := stmt.(*ast.AssignStmt)
			if ok && len(assign.Lhs) == 1 && auditNodeText(assign.Lhs[0]) == "checks" && len(assign.Rhs) == 1 {
				guard = auditGuardTable(assign.Rhs[0])
			}
			rangeStmt, ok := stmt.(*ast.RangeStmt)
			if ok && auditNodeText(rangeStmt.X) == "checks" && rangeStmt.Value != nil && auditNodeText(rangeStmt.Value) == "c" {
				refuse = auditGuardRefusal(rangeStmt.Body)
			}
		}
		return guard && refuse
	}
	return false
}

func auditGuardTable(e ast.Expr) bool {
	table, ok := e.(*ast.CompositeLit)
	if !ok {
		return false
	}
	for _, e := range table.Elts {
		row, ok := e.(*ast.CompositeLit)
		if ok && len(row.Elts) == 2 && auditNodeText(row.Elts[0]) == "cfg.EgressToken.Class() != egress.EgressClassHook" {
			return true
		}
	}
	return false
}

func auditGuardRefusal(body *ast.BlockStmt) bool {
	for _, stmt := range body.List {
		cond, ok := stmt.(*ast.IfStmt)
		if !ok || auditNodeText(cond.Cond) != "c.bad" || len(cond.Body.List) != 1 {
			continue
		}
		ret, ok := cond.Body.List[0].(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			continue
		}
		call, ok := ret.Results[0].(*ast.CallExpr)
		if ok && auditNodeText(call.Fun) == "cascade.New" && len(call.Args) == 2 && auditNodeText(call.Args[0]) == "cascade.KindInvalidInput" {
			return true
		}
	}
	return false
}

func auditHookIntercept(f *ast.File) bool {
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "interceptParams" || fn.Body == nil {
			continue
		}
		typed := false
		for _, field := range fn.Type.Params.List {
			for _, name := range field.Names {
				if name.Name == "token" && auditNodeText(field.Type) == "egress.Capability" {
					typed = true
				}
			}
		}
		if !typed {
			return false
		}
		found := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if ok && auditNodeText(call.Fun) == "firewall.Intercept" && len(call.Args) == 4 && auditNodeText(call.Args[1]) == "token" {
				found = true
			}
			return true
		})
		return found
	}
	return false
}
