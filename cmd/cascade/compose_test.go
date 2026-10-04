// Purpose: the CLI composition root's tests: the generated root-noun golden
//
//	and the proof that root_mounts.go owns the one mount list.
//
// Constraints: testdata/golden_root_nouns.txt is generated, never hand-edited:
//
//	regenerate it with CASCADE_TESTKIT_UPDATE_GOLDEN=1 (internal/testkit's
//	update switch, refused when CI is set).
//
// SPORT: cmd/cascade composition root tests (P1-CORE-01).
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/testkit"
)

// compareOrUpdateGolden compares got with the golden file at path, or rewrites
// it in testkit's update mode (never when CI is set, matching testkit.Golden).
func compareOrUpdateGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	if testkit.UpdateRequested() {
		if os.Getenv("CI") != "" {
			t.Fatalf("refusing to update golden %s: CI is set", path)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (regenerate with %s=1): %v", path, "CASCADE_TESTKIT_UPDATE_GOLDEN", err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("golden mismatch for %s\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}

// renderRootNouns lists every direct child of newRootCmd(), hidden ones
// included: name, Hidden flag, sorted aliases.
func renderRootNouns() []byte {
	var lines []string
	for _, c := range newRootCmd().Commands() {
		aliases := append([]string(nil), c.Aliases...)
		sort.Strings(aliases)
		lines = append(lines, fmt.Sprintf("%s hidden=%t aliases=%s", c.Name(), c.Hidden, strings.Join(aliases, ",")))
	}
	sort.Strings(lines)
	return []byte(strings.Join(lines, "\n") + "\n")
}

func TestGoldenRootNouns(t *testing.T) {
	got := renderRootNouns()
	if len(got) < 100 {
		t.Fatalf("root noun record is implausibly small: %q", got)
	}
	compareOrUpdateGolden(t, "testdata/golden_root_nouns.txt", got)
}

// parseCmdFile parses one non-test source file of this package.
func parseCmdFile(t *testing.T, name string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return f
}

// funcDecls returns every top-level function declaration named name in the
// non-test files of this package, keyed by file.
func funcDecls(t *testing.T, name string) map[string]*ast.FuncDecl {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	found := map[string]*ast.FuncDecl{}
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		for _, d := range parseCmdFile(t, n).Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == name {
				found[n] = fd
			}
		}
	}
	return found
}

// countCalls counts calls to the plain identifier name inside n.
func countCalls(n ast.Node, name string) int {
	count := 0
	ast.Inspect(n, func(x ast.Node) bool {
		if c, ok := x.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok && id.Name == name {
				count++
			}
		}
		return true
	})
	return count
}

func lineCount(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return bytes.Count(b, []byte("\n"))
}

// TestRootMountsOwnsMountList proves the mount list has one home: mountSubcommands
// is declared only in root_mounts.go, newRootCmd calls it exactly once, and
// root.go and root_mounts.go stay inside their size budgets.
func TestRootMountsOwnsMountList(t *testing.T) {
	decls := funcDecls(t, "mountSubcommands")
	if len(decls) != 1 || decls["root_mounts.go"] == nil {
		t.Fatalf("mountSubcommands must be declared only in root_mounts.go, found in %d files: %v", len(decls), decls)
	}
	root := funcDecls(t, "newRootCmd")["root.go"]
	if root == nil {
		t.Fatal("root.go declares no newRootCmd")
	}
	if got := countCalls(root, "mountSubcommands"); got != 1 {
		t.Errorf("newRootCmd calls mountSubcommands %d times, want exactly 1", got)
	}
	if n := lineCount(t, "root.go"); n > 270 {
		t.Errorf("root.go has %d lines, want <= 270", n)
	}
	if n := lineCount(t, "root_mounts.go"); n > 300 {
		t.Errorf("root_mounts.go has %d lines, want <= 300", n)
	}
	if len(rootMounts) == 0 {
		t.Error("no root mounts registered")
	}
}
