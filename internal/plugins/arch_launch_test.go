// Purpose: architecture gates for the trusted launch: outside the process
// package itself, process.TrustTierTrusted is named and a process runtime's
// Launch is called only in internal/plugins/trusted_launch.go; and every
// UpdatePlugin caller and the plugin.update registration pass the real
// process handshaker (source scans with go/ast over non-test files).
// SPORT: internal/plugins trusted launch architecture tests (P1-PLG-03).
package plugins

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const processImportPath = "github.com/acamarata/cascade/internal/plugins/process"

// parseTree calls visit for every non-test Go file under the repo roots.
func parseTree(t *testing.T, roots []string, visit func(rel string, file *ast.File)) {
	t.Helper()
	for _, root := range roots {
		err := filepath.WalkDir(filepath.Join("..", "..", root), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			file, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if perr != nil {
				return perr
			}
			rel, _ := filepath.Rel(filepath.Join("..", ".."), path)
			visit(filepath.ToSlash(rel), file)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// processAlias is the name file imports the process package under, or "".
func processAlias(file *ast.File) string {
	for _, imp := range file.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == processImportPath {
			if imp.Name != nil {
				return imp.Name.Name
			}
			return "process"
		}
	}
	return ""
}

func TestOnlyLaunchInstalledAssignsTrusted(t *testing.T) {
	const allowed = "internal/plugins/trusted_launch.go"
	var trusted, launches []string
	var allowedSeen [2]bool
	parseTree(t, []string{"cmd", "internal", "plugins"}, func(rel string, file *ast.File) {
		alias := processAlias(file)
		if alias == "" || strings.HasPrefix(rel, "internal/plugins/process/") {
			return
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if id, ok := x.X.(*ast.Ident); ok && id.Name == alias && x.Sel.Name == "TrustTierTrusted" {
					allowedSeen[0] = allowedSeen[0] || rel == allowed
					if rel != allowed {
						trusted = append(trusted, rel)
					}
				}
			case *ast.CallExpr:
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Launch" {
					allowedSeen[1] = allowedSeen[1] || rel == allowed
					if rel != allowed {
						launches = append(launches, rel)
					}
				}
			}
			return true
		})
	})
	if !allowedSeen[0] || !allowedSeen[1] {
		t.Fatalf("positive control: %s must assign process.TrustTierTrusted and call Launch (seen %v)", allowed, allowedSeen)
	}
	if len(trusted) != 0 || len(launches) != 0 {
		t.Fatalf("process.TrustTierTrusted outside %s: %v; a Launch call outside it in a process importer: %v", allowed, trusted, launches)
	}
}

func TestUpdateCallersPassHandshaker(t *testing.T) {
	isHandshaker := func(e ast.Expr) bool {
		if call, ok := e.(*ast.CallExpr); ok {
			e = call.Fun
		}
		sel, ok := e.(*ast.SelectorExpr)
		return ok && strings.HasPrefix(sel.Sel.Name, "NewProcessHandshaker")
	}
	found := map[string]int{}
	parseTree(t, []string{"cmd"}, func(rel string, file *ast.File) {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			sel, isSel := callSel(call, ok)
			switch {
			case isSel && sel == "UpdatePlugin" && len(call.Args) == 8:
				if !isHandshaker(call.Args[3]) {
					t.Errorf("%s: UpdatePlugin without NewProcessHandshaker()", rel)
				}
				found[rel]++
			case isSel && sel == "UpdateRPCHandler" && len(call.Args) == 2:
				if !isHandshaker(call.Args[1]) {
					t.Errorf("%s: UpdateRPCHandler without NewProcessHandshaker", rel)
				}
				found[rel]++
			}
			return true
		})
	})
	for _, want := range []string{"cmd/cascade/plugin_update.go", "cmd/cascade/plugin_update_registry.go", "cmd/cascade/wire_plugin_update.go"} {
		if found[want] == 0 {
			t.Errorf("%s: no UpdatePlugin/UpdateRPCHandler call found (scan saw %v)", want, found)
		}
	}
}

// callSel returns the selected name of call's function, if it is a selector.
func callSel(call *ast.CallExpr, ok bool) (string, bool) {
	if !ok {
		return "", false
	}
	sel, isSel := call.Fun.(*ast.SelectorExpr)
	if !isSel {
		return "", false
	}
	return sel.Sel.Name, true
}
