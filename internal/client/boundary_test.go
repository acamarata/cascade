// Purpose: the cmd-rpc-server-boundary depguard rule's own seeded-violation
//
//	proof (hard requirement: "Add a seeded-violation test proving the rule
//	fires") — split out of client_test.go to stay under the 300-line file
//	cap. Mirrors internal/build/arch_test.go's own established pattern
//	(archScan/archViolations: a hand-written Go-level import scanner run
//	against both the real tree and a materialized seeded-violation
//	fixture) rather than shelling out to golangci-lint, for the same
//	reason that file gives: the depguard rule and this scanner must
//	independently agree on the same violation, as a second enforcement
//	layer. internal/build itself is out of this ticket's files_scope to
//	edit, so this proof lives here instead of alongside arch_test.go's
//	other rules.
//
// SPORT: internal/client (ADD, per T-3 sport_updates).
package client

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// clientModuleRoot locates the repo root by walking up from this file.
func clientModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("boundary gate: runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("boundary gate: no go.mod found walking up from %s", file)
		}
		dir = parent
	}
}

// cmdRPCBoundaryExempt mirrors .golangci.yml's cmd-rpc-server-boundary
// rule's exemption list exactly — the three composition-root files
// legitimately importing internal/rpc for reasons other than a
// hand-rolled outbound call.
var cmdRPCBoundaryExempt = map[string]bool{
	"daemon_unix_run.go": true,
	"mcp.go":             true,
	"elevate_helper.go":  true,
}

// scanCmdCascadeRPCImports returns, sorted, the base filenames under dir
// (a cmd/cascade directory, real or a materialized fixture) whose
// non-test .go source imports internal/rpc and is not on the exemption
// list.
func scanCmdCascadeRPCImports(t *testing.T, dir string) []string {
	t.Helper()
	var violations []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("boundary gate: reading %s: %v", dir, err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if cmdRPCBoundaryExempt[name] {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("boundary gate: parsing %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if path == "github.com/acamarata/cascade/internal/rpc" {
				violations = append(violations, name)
			}
		}
	}
	return violations
}

// TestCmdRPCServerBoundary_RealTreeGreen asserts no non-exempt
// cmd/cascade file imports internal/rpc on the real tree today — status.go
// no longer does, after this ticket's rewrite.
func TestCmdRPCServerBoundary_RealTreeGreen(t *testing.T) {
	root := clientModuleRoot(t)
	dir := filepath.Join(root, "cmd", "cascade")
	v := scanCmdCascadeRPCImports(t, dir)
	if len(v) != 0 {
		t.Fatalf("cmd-rpc-server-boundary violated on real tree: %v", v)
	}
}

// TestCmdRPCServerBoundary_SeededViolationRed materializes a
// cmd/cascade-shaped fixture with one non-exempt file hand-importing
// internal/rpc and proves the scanner (independently: the same rule
// .golangci.yml's depguard entry encodes) flags it.
func TestCmdRPCServerBoundary_SeededViolationRed(t *testing.T) {
	dir := t.TempDir()
	src := `package main

import (
	"github.com/acamarata/cascade/internal/rpc"
)

var _ = rpc.RPCPath
`
	if err := os.WriteFile(filepath.Join(dir, "hand_rolled.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	v := scanCmdCascadeRPCImports(t, dir)
	if len(v) == 0 {
		t.Fatal("cmd-rpc-server-boundary: expected a violation in the seeded fixture, found none")
	}
}

// TestCmdRPCServerBoundary_ExemptFilesAllowed proves the same scanner does
// NOT flag one of the three named composition-root exemptions, so the
// rule's narrowing is itself exercised, not just its denial.
func TestCmdRPCServerBoundary_ExemptFilesAllowed(t *testing.T) {
	dir := t.TempDir()
	src := `package main

import (
	"github.com/acamarata/cascade/internal/rpc"
)

var _ = rpc.RPCPath
`
	if err := os.WriteFile(filepath.Join(dir, "mcp.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	v := scanCmdCascadeRPCImports(t, dir)
	if len(v) != 0 {
		t.Fatalf("cmd-rpc-server-boundary: exempt file mcp.go was flagged: %v", v)
	}
}
