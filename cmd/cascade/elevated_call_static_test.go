package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
)

func TestElevatedCLIVerbsUseCallElevated(t *testing.T) {
	files, constants := elevatedSources(t)
	if len(files) == 0 || len(constants) == 0 {
		t.Fatal("empty source inventory")
	}
	for path, file := range files {
		for _, violation := range elevatedViolations(path, file, constants) {
			t.Errorf("%s: %s", path, violation)
		}
	}
	for _, src := range []string{
		`package main; func seed(){ c.Do(ctx, "plugin.add", nil, nil) }`,
		`package main; const method = "plugin.add"; func seed(){ c.Do(ctx, method, nil, nil) }`,
		`package main; import "github.com/acamarata/cascade/internal/client"; func seed(){ c.Do(ctx, client.ApprovalGrantMethod, nil, nil) }`,
		`package main; import cl "github.com/acamarata/cascade/internal/client"; func seed(){ c.Do(ctx, cl.ApprovalGrantMethod, nil, nil) }`,
		`package main; func seed(){ c.DoElevated(ctx, method, nil, nil, nil) }`,
		`package main; func seed(){ c.ApprovalGrant(ctx, p, nil) }`,
		`package main; func seed(){ c.StandingGrantCreate(ctx, p, nil) }`,
		`package main; func seed(){ c.StandingGrantChange(ctx, p, nil) }`,
		`package main; func seed(){ (*client.Client).ApprovalGrant(c, ctx, p, nil) }`,
		`package main; func seed(){ c.StandingGrantCreate(args()) }`,
	} {
		file, err := parser.ParseFile(token.NewFileSet(), "seed.go", src, 0)
		if err != nil {
			t.Fatal(err)
		}
		seedConstants := make(map[string]ast.Expr, len(constants))
		for k, v := range constants {
			seedConstants[k] = v
		}
		collectElevatedConstants(file, seedConstants)
		if len(elevatedViolations("cmd/cascade/seed.go", file, seedConstants)) != 1 {
			t.Fatalf("seed escaped: %s", src)
		}
	}
}

func elevatedSources(t *testing.T) (map[string]*ast.File, map[string]ast.Expr) {
	t.Helper()
	files := make(map[string]*ast.File)
	constants := make(map[string]ast.Expr)
	for _, dir := range []string{".", "../../internal/client"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			prefix := "cmd/cascade/"
			if dir != "." {
				prefix = "internal/client/"
			}
			files[prefix+entry.Name()] = file
			collectElevatedConstants(file, constants)
		}
	}
	return files, constants
}

func collectElevatedConstants(file *ast.File, constants map[string]ast.Expr) {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		var previous []ast.Expr
		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)
			if len(value.Values) != 0 {
				previous = value.Values
			}
			for i, name := range value.Names {
				if i < len(previous) {
					constants[file.Name.Name+"."+name.Name] = previous[i]
				}
			}
		}
	}
}

func elevatedMethod(expr ast.Expr, pkg string, constants map[string]ast.Expr, seen map[string]bool) string {
	switch v := expr.(type) {
	case *ast.BasicLit:
		if v.Kind == token.STRING {
			s, _ := strconv.Unquote(v.Value)
			return s
		}
	case *ast.Ident:
		key := pkg + "." + v.Name
		if !seen[key] {
			seen[key] = true
			return elevatedMethod(constants[key], pkg, constants, seen)
		}
	case *ast.SelectorExpr:
		if id, ok := v.X.(*ast.Ident); ok {
			return elevatedMethod(v.Sel, id.Name, constants, seen)
		}
	case *ast.BinaryExpr:
		if v.Op == token.ADD {
			return elevatedMethod(v.X, pkg, constants, seen) + elevatedMethod(v.Y, pkg, constants, seen)
		}
	case *ast.ParenExpr:
		return elevatedMethod(v.X, pkg, constants, seen)
	}
	return ""
}

// elevatedWrappers are the internal/client wrappers that take an attester;
// a command reaching one directly bypasses callElevated's custody signing.
// Their signature is (ctx, params, attest), so a two-argument call cannot be
// them: approval_standing.go's standingWriter interface predates the attester
// and cannot reach *client.Client. Any other arity (a method expression with
// the receiver first, a spread call) is flagged.
var elevatedWrappers = map[string]bool{"ApprovalGrant": true, "StandingGrantCreate": true, "StandingGrantChange": true}

func elevatedViolations(path string, file *ast.File, constants map[string]ast.Expr) []string {
	constants = elevatedImportAliases(file, constants)
	var violations []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if selector.Sel.Name == "DoElevated" && path != "cmd/cascade/elevated_call.go" && path != "internal/client/elevated.go" && path != "internal/client/policy.go" {
			violations = append(violations, "DoElevated outside the elevated call boundary")
		}
		if elevatedWrappers[selector.Sel.Name] && len(call.Args) != 2 && strings.HasPrefix(path, "cmd/cascade/") {
			violations = append(violations, selector.Sel.Name+" lets a command supply its own attester; use callElevated")
		}
		if selector.Sel.Name == "Do" && len(call.Args) > 1 {
			method := elevatedMethod(call.Args[1], file.Name.Name, constants, make(map[string]bool))
			if rpc.IsElevatedVerb(method) {
				violations = append(violations, "Do bypasses callElevated for "+method)
			}
		}
		return true
	})
	return violations
}

func elevatedImportAliases(file *ast.File, constants map[string]ast.Expr) map[string]ast.Expr {
	resolved := make(map[string]ast.Expr, len(constants))
	for key, value := range constants {
		resolved[key] = value
	}
	for _, imp := range file.Imports {
		if imp.Name == nil {
			continue
		}
		path, _ := strconv.Unquote(imp.Path.Value)
		pkg := filepath.Base(path)
		for key, value := range constants {
			if strings.HasPrefix(key, pkg+".") {
				resolved[imp.Name.Name+strings.TrimPrefix(key, pkg)] = value
			}
		}
	}
	return resolved
}
