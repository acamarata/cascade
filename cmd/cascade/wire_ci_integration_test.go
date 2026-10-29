package main

// TestSchedulerDispatcherIntegration guards the layering of the CI
// checkpoint path (P1-CI-10 acceptance [6], verification[5]): the producer
// lives in internal/jobs and never depends on internal/ci, and the consumer
// that turns a checkpoint into a Dispatcher call lives in the daemon layer
// (cmd/cascade/wire_ci.go). wire_ci.go is !windows, so this untagged test
// reads source with go/parser only and compiles on every platform.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const ciModulePrefix = "github.com/acamarata/cascade/"

// moduleImports returns the module-internal imports of every non-test Go
// file in dir, build tags ignored (a superset, so the guard is conservative).
func moduleImports(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasPrefix(p, ciModulePrefix) {
				out = append(out, strings.TrimPrefix(p, ciModulePrefix))
			}
		}
	}
	return out
}

// jobsClosureReaches walks internal/jobs' module-internal import closure and
// returns the import chain that reaches target, or nil, plus the closure size.
func jobsClosureReaches(t *testing.T, root, target string) ([]string, int) {
	t.Helper()
	parent := map[string]string{"internal/jobs": ""}
	queue := []string{"internal/jobs"}
	for len(queue) > 0 {
		pkg := queue[0]
		queue = queue[1:]
		for _, imp := range moduleImports(t, filepath.Join(root, filepath.FromSlash(pkg))) {
			if _, ok := parent[imp]; ok {
				continue
			}
			parent[imp] = pkg
			if imp == target {
				chain := []string{imp}
				for p := pkg; p != ""; p = parent[p] {
					chain = append([]string{p}, chain...)
				}
				return chain, len(parent)
			}
			queue = append(queue, imp)
		}
	}
	return nil, len(parent)
}

// wireCIUses reports the package-qualified identifiers (pkg.Name) and the
// called method names in cmd/cascade/wire_ci.go.
func wireCIUses(t *testing.T) (qualified, methods map[string]bool) {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "wire_ci.go", nil, 0)
	if err != nil {
		t.Fatalf("parse wire_ci.go: %v", err)
	}
	qualified, methods = map[string]bool{}, map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok {
				qualified[id.Name+"."+x.Sel.Name] = true
			}
		case *ast.CallExpr:
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
				methods[sel.Sel.Name] = true
			} else if id, ok := x.Fun.(*ast.Ident); ok {
				methods[id.Name] = true
			}
		}
		return true
	})
	return qualified, methods
}

func TestSchedulerDispatcherIntegration(t *testing.T) {
	root := filepath.Join("..", "..")
	chain, size := jobsClosureReaches(t, root, "internal/ci")
	if chain != nil {
		t.Fatalf("internal/jobs depends on internal/ci via %s", strings.Join(chain, " -> "))
	}
	if size < 4 {
		t.Fatalf("internal/jobs import closure has %d packages; the walk found nothing to check", size)
	}
	qualified, methods := wireCIUses(t)
	for _, want := range []string{"jobs.EventLeaseCheckpoint", "jobs.NewCheckpointWatcher", "daemon.NewCISubsystem"} {
		if !qualified[want] {
			t.Errorf("cmd/cascade/wire_ci.go does not use %s: the checkpoint consumer is not in the daemon layer", want)
		}
	}
	for _, want := range []string{"registerDaemonWiring", "Resume", "Checkpoint", "GoSupervised"} {
		if !methods[want] {
			t.Errorf("cmd/cascade/wire_ci.go never calls %s", want)
		}
	}
}
