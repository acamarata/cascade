package build

// Purpose: TestAgentEnvGate, the agent-environment arch gate (P1-AGT-03): no
// non-test code under providers/agents or internal/providers/agents reads the
// whole daemon environment (os.Environ or syscall.Environ), so a child's
// environment can only be the exact list the shim builds. os.Getenv of one
// named variable is allowed; the credential table decides which names.
// Constraints: typed by import name, so an aliased import is still caught;
// testdata trees and _test.go files are out of reach.
// SPORT: internal.build tests (EXTEND) - P1-AGT-03.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// agentEnvRoots are the trees the gate covers, relative to the module root.
var agentEnvRoots = []string{"providers/agents", "internal/providers/agents"}

// scanEnvironCalls reports every whole-environment read under the roots of
// module as "file:line", and how many files it parsed.
func scanEnvironCalls(module string, roots []string) (hits []string, files int, err error) {
	for _, r := range roots {
		werr := filepath.WalkDir(filepath.Join(module, r), func(p string, d fs.DirEntry, werr error) error {
			switch {
			case werr != nil:
				return werr
			case d.IsDir() && d.Name() == "testdata":
				return filepath.SkipDir
			case d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go"):
				return nil
			}
			found, perr := environCallsIn(p)
			files++
			hits = append(hits, found...)
			return perr
		})
		if werr != nil {
			return nil, files, werr
		}
	}
	sort.Strings(hits)
	return hits, files, nil
}

// environCallsIn parses one file and returns its whole-environment reads.
func environCallsIn(path string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	local := map[string]string{} // local import name -> package path
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if p != "os" && p != "syscall" {
			continue
		}
		name := p
		if imp.Name != nil {
			name = imp.Name.Name
		}
		local[name] = p
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Environ" {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && local[id.Name] != "" {
			out = append(out, path+":"+strconv.Itoa(fset.Position(sel.Pos()).Line))
		}
		return true
	})
	return out, nil
}

func TestAgentEnvGate(t *testing.T) {
	hits, files, err := scanEnvironCalls(moduleRoot(t), agentEnvRoots)
	if err != nil {
		t.Fatal(err)
	}
	if files < 10 {
		t.Fatalf("the gate parsed %d files under %v; a vacuous scan proves nothing", files, agentEnvRoots)
	}
	if len(hits) != 0 {
		t.Fatalf("whole-environment reads under providers/agents (a child's environment is built, never inherited): %v", hits)
	}
}

func TestAgentEnvGateCatchesSeededReads(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("providers/agents/a/a.go", "package a\n\nimport \"os\"\n\nvar E = os.Environ()\n")
	write("providers/agents/a/b.go", "package a\n\nimport sys \"syscall\"\n\nvar F = sys.Environ()\n")
	write("providers/agents/a/ok.go", "package a\n\nimport \"os\"\n\nvar G = os.Getenv(\"PATH\")\n")
	write("providers/agents/a/a_test.go", "package a\n\nimport \"os\"\n\nvar H = os.Environ()\n")
	write("providers/agents/a/testdata/x/main.go", "package main\n\nimport \"os\"\n\nvar I = os.Environ()\n")
	hits, files, err := scanEnvironCalls(dir, []string{"providers/agents"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || files != 3 || !strings.HasSuffix(hits[0], "a.go:5") || !strings.HasSuffix(hits[1], "b.go:5") {
		t.Fatalf("hits %v over %d files; want a.go:5 and b.go:5 over 3 files", hits, files)
	}
}
