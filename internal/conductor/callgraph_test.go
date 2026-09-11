// Purpose: the task 8 production call-graph test, independent of
//
//	seam_test.go's file-level scan: it names the CALLING FUNCTION for every
//	direct pkg/provider.ModelProvider verb invocation found in the module's
//	non-test .go files, so a violation inside internal/conductor itself
//	(a future function other than the door's own dispatch path) is caught
//	with the same rigor as one outside the package entirely.
package conductor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// callgraphEdge is one call-graph edge: a calling function's qualified
// name to the ModelProvider verb it invoked.
type callgraphEdge struct {
	caller string
	verb   string
}

func callgraphFuncName(fd *ast.FuncDecl) string {
	if fd.Recv != nil && len(fd.Recv.List) > 0 {
		switch t := fd.Recv.List[0].Type.(type) {
		case *ast.StarExpr:
			if id, ok := t.X.(*ast.Ident); ok {
				return id.Name + "." + fd.Name.Name
			}
		case *ast.Ident:
			return t.Name + "." + fd.Name.Name
		}
	}
	return fd.Name.Name
}

// callgraphEdgesInFile finds every (callerFuncName, verb) edge in file for
// receiver identifiers in vars, using the same identifier heuristic
// seam_test.go's seamProviderVars/seamRightmostIdent build.
func callgraphEdgesInFile(file *ast.File, vars map[string]bool) []callgraphEdge {
	var edges []callgraphEdge
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		name := callgraphFuncName(fd)
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !seamVerbs[sel.Sel.Name] {
				return true
			}
			recv, ok := seamRightmostIdent(sel.X)
			if !ok || !vars[recv] {
				return true
			}
			edges = append(edges, callgraphEdge{caller: name, verb: sel.Sel.Name})
			return true
		})
	}
	return edges
}

// TestExecute_ProviderCallGraph is the production call-graph analysis over
// the module's non-test .go files: conductor.Execute/ExecuteStream (via
// Pipeline.capability, the door's sole dispatch helper) are the only
// permitted callers of a pkg/provider.ModelProvider verb. See
// TestExecute_ProviderCallGraphCatchesSeededViolation for proof this scan
// can fail.
func TestExecute_ProviderCallGraph(t *testing.T) {
	root := seamModuleRoot(t)
	fset := token.NewFileSet()
	allowed := map[string]bool{"Pipeline.capability": true, "Pipeline.embedCapability": true}
	var bad []string
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if strings.Contains(rel, string(filepath.Separator)+".") || strings.Contains(rel, "testdata") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		alias := seamProviderAlias(file)
		if alias == "" {
			return nil
		}
		vars := seamProviderVars(file, alias)
		inConductor := strings.HasPrefix(rel, "internal"+string(filepath.Separator)+"conductor"+string(filepath.Separator))
		for _, e := range callgraphEdgesInFile(file, vars) {
			if inConductor && allowed[e.caller] {
				continue
			}
			bad = append(bad, rel+": "+e.caller+" -> ModelProvider."+e.verb)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("callgraph gate: walking module tree: %v", walkErr)
	}
	if len(bad) == 0 {
		return
	}
	for _, e := range bad {
		t.Logf("  %s", e)
	}
	t.Errorf("%d caller(s) reach pkg/provider.ModelProvider outside the door's own dispatch path (see seam_test.go's matching, documented R-40.X10 finding)", len(bad))
}

// TestExecute_ProviderCallGraphCatchesSeededViolation proves the scan can
// fail: a synthetic second-door caller, parsed as a standalone fixture
// (never compiled into the module - Art.7.1), is detected by the same
// edge-extraction the production scan above uses.
func TestExecute_ProviderCallGraphCatchesSeededViolation(t *testing.T) {
	const fixture = `package seeded

import provider "github.com/acamarata/cascade/pkg/provider"

type Sidecar struct {
	mp provider.ModelProvider
}

func (s *Sidecar) Bypass(ctx, req any) {
	s.mp.Chat(ctx, req)
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "seeded_fixture.go", fixture, 0)
	if err != nil {
		t.Fatalf("parsing seeded fixture: %v", err)
	}
	alias := seamProviderAlias(file)
	if alias == "" {
		t.Fatal("seeded fixture: provider import not detected")
	}
	vars := seamProviderVars(file, alias)
	edges := callgraphEdgesInFile(file, vars)
	if len(edges) != 1 || edges[0].caller != "Sidecar.Bypass" || edges[0].verb != "Chat" {
		t.Fatalf("seeded violation not caught: got %+v, want exactly one Sidecar.Bypass -> Chat edge", edges)
	}
}
