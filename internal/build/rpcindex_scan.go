package build

import (
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
)

// ScanDeclaredAccess scans tracked production Go files for static access
// declarations. Unresolved entries have the form "file:line: reason".
func ScanDeclaredAccess(repoRoot string) (map[string]string, []string, error) {
	return rpcScanDeclarations(repoRoot, false)
}

// ScanElevatedVerbs scans direct registrations and file-local table-fed
// registrations. Unresolved entries have the form "file:line: reason".
func ScanElevatedVerbs(repoRoot string) (map[string]string, []string, error) {
	return rpcScanDeclarations(repoRoot, true)
}

type rpcDeclarationScan struct {
	root       string
	cache      map[string]constLiteralMap
	values     map[string]string
	unresolved []string
	elevated   bool
}

func rpcScanDeclarations(root string, elevated bool) (map[string]string, []string, error) {
	sources, cache, err := rpcParseSources(root)
	if err != nil {
		return nil, nil, err
	}
	s := rpcDeclarationScan{root: root, cache: cache, values: map[string]string{}, elevated: elevated}
	name := "DeclareAccess"
	if elevated {
		name = "RegisterElevatedVerb"
	}
	for _, source := range sources {
		tables := rpcTableCalls(source.file)
		ast.Inspect(source.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !rpcSymbol(root, source.rel, call.Fun, name) {
				return true
			}
			if rows, exists := tables[call]; elevated && exists {
				for _, args := range rows {
					s.record(source, call, args)
				}
			} else {
				s.record(source, call, call.Args)
			}
			return true
		})
	}
	return s.values, s.unresolved, nil
}

func rpcSymbol(root, rel string, expr ast.Expr, name string) bool {
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name == name && filepath.ToSlash(filepath.Dir(rel)) == "internal/rpc"
	}
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	dir, ok := resolveImportDir(root, rel, id.Name)
	return ok && dir == "internal/rpc"
}

func (s *rpcDeclarationScan) record(src rpcSource, call *ast.CallExpr, args []ast.Expr) {
	problem := func(reason string) {
		s.unresolved = append(s.unresolved, fmt.Sprintf("%s:%d: %s", src.rel, src.fset.Position(call.Pos()).Line, reason))
	}
	if len(args) != 2 {
		problem("expected two arguments")
		return
	}
	method, ok := resolveMethodArg(s.root, src.rel, args[0], s.cache)
	if !ok || method == "" {
		problem("unresolved method")
		return
	}
	value := ""
	if s.elevated {
		value = rpcElevationRule(s.root, src.rel, args[1])
	} else {
		value = rpcAccessClass(s.root, src.rel, args[1])
	}
	if value == "" {
		problem("unresolved declaration for " + method)
		s.values[method] = ""
		return
	}
	previous, exists := s.values[method]
	if exists && previous == "" {
		return
	}
	if exists && previous != value {
		s.values[method] = ""
		if s.elevated {
			problem("conflicting elevation for " + method)
		} else {
			problem("conflicting classes: " + method)
		}
		return
	}
	s.values[method] = value
}

func rpcAccessClass(root, rel string, expr ast.Expr) string {
	for _, class := range []string{"Open", "OwnerOnly", "OwnerView", "Scoped", "Ingest"} {
		if rpcSymbol(root, rel, expr, "Access"+class) {
			return class
		}
	}
	return ""
}

func rpcElevationRule(root, rel string, expr ast.Expr) string {
	literal, ok := expr.(*ast.CompositeLit)
	if !ok || !rpcSymbol(root, rel, literal.Type, "ElevationRule") {
		return ""
	}
	always, conditional, valid := false, false, true
	seen := map[string]bool{}
	for _, element := range literal.Elts {
		kv, ok := element.(*ast.KeyValueExpr)
		if !ok {
			return ""
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || seen[key.Name] {
			return ""
		}
		seen[key.Name] = true
		switch key.Name {
		case "Always":
			id, ok := kv.Value.(*ast.Ident)
			valid = ok && id.Name == "true"
			always = true
		case "Conditional":
			conditional = true
		default:
			return ""
		}
	}
	if !valid || always == conditional {
		return ""
	}
	if always {
		return "always"
	}
	return "conditional"
}

// rpcTableCalls expands only package variables of positional two-field rows
// used by a range's value variable as the method and rule arguments.
func rpcTableCalls(file *ast.File) map[*ast.CallExpr][][]ast.Expr {
	tables := map[string]*ast.CompositeLit{}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if i < len(vs.Values) {
					if lit, ok := vs.Values[i].(*ast.CompositeLit); ok {
						tables[name.Name] = lit
					}
				}
			}
		}
	}
	out := map[*ast.CallExpr][][]ast.Expr{}
	ast.Inspect(file, func(n ast.Node) bool {
		r, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		x, xok := r.X.(*ast.Ident)
		value, vok := r.Value.(*ast.Ident)
		key, kok := r.Key.(*ast.Ident)
		if !xok || !vok || !kok || key.Name != "_" || r.Tok != token.DEFINE {
			return true
		}
		rows, fields := rpcTableRows(tables[x.Name])
		if len(rows) == 0 {
			return true
		}
		ast.Inspect(r.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if ok && len(call.Args) == 2 && rpcRowField(call.Args[0], value.Name, fields[0]) && rpcRowField(call.Args[1], value.Name, fields[1]) {
				out[call] = rows
			}
			return true
		})
		return true
	})
	return out
}

func rpcRowField(expr ast.Expr, variable, field string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == variable && sel.Sel.Name == field
}

func rpcTableRows(table *ast.CompositeLit) ([][]ast.Expr, []string) {
	if table == nil {
		return nil, nil
	}
	array, ok := table.Type.(*ast.ArrayType)
	if !ok {
		return nil, nil
	}
	rowType, ok := array.Elt.(*ast.StructType)
	if !ok {
		return nil, nil
	}
	var fields []string
	for _, f := range rowType.Fields.List {
		for _, name := range f.Names {
			fields = append(fields, name.Name)
		}
	}
	if len(fields) != 2 {
		return nil, nil
	}
	var rows [][]ast.Expr
	for _, element := range table.Elts {
		row, ok := element.(*ast.CompositeLit)
		if !ok || len(row.Elts) != 2 {
			return nil, nil
		}
		for _, expr := range row.Elts {
			if _, keyed := expr.(*ast.KeyValueExpr); keyed {
				return nil, nil
			}
		}
		rows = append(rows, row.Elts)
	}
	return rows, fields
}
