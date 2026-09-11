package build

// Purpose: proves CheckRPCMethodCoverage against the REAL current tree
// (R-16.80 Ruling 3(c)): every resolvable cmd/cascade `.Do` call site
// must have a matching Register(...) somewhere, and this test names the
// exact two known-unresolvable call sites so a THIRD one appearing later
// is a test failure, not a silent widening of the blind spot.
// SPORT: internal.build.CheckRPCMethodCoverage/ADDED (R-16.80).

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func rpcMethodGateModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("rpc method gate: runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("rpc method gate: no go.mod found walking up from %s", file)
		}
		dir = parent
	}
}

// TestRPCMethodGate_RealTreeGreen proves conductor.execute (R-16.80's own
// defect) and every other resolvable cmd/cascade .Do call site now has a
// real Register(...) counterpart, and that the two known dynamic-method
// call sites (memory.go, recall.go) are exactly the unresolved set - no
// more, no fewer.
func TestRPCMethodGate_RealTreeGreen(t *testing.T) {
	root := rpcMethodGateModuleRoot(t)
	files, err := ListTrackedFiles(root)
	if err != nil {
		t.Fatalf("rpc method gate: %v", err)
	}
	violations, unresolved, err := CheckRPCMethodCoverageTracked(root, files)
	if err != nil {
		t.Fatalf("rpc method gate: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("rpc method gate: %d unregistered method(s):\n%v", len(violations), violations)
	}
	if len(unresolved) != 2 {
		t.Fatalf("rpc method gate: want exactly 2 disclosed unresolved .Do call sites "+
			"(cmd/cascade/memory.go, cmd/cascade/recall.go), got %d: %v", len(unresolved), unresolved)
	}
	wantFiles := map[string]bool{
		filepath.FromSlash("cmd/cascade/memory.go"): false,
		filepath.FromSlash("cmd/cascade/recall.go"): false,
	}
	for _, u := range unresolved {
		if _, ok := wantFiles[u.File]; !ok {
			t.Errorf("rpc method gate: unexpected unresolved call site %s:%d - the disclosed blind spot widened", u.File, u.Line)
			continue
		}
		wantFiles[u.File] = true
	}
	for f, seen := range wantFiles {
		if !seen {
			t.Errorf("rpc method gate: expected an unresolved .Do call site in %s, found none", f)
		}
	}
}

// TestRPCMethodGate_CatchesUnregisteredMethod is the seeded-violation
// proof: a fabricated .Do call naming a method nothing registers (the
// exact shape "conductor.execute" was before this ticket) must be
// reported by CheckRPCMethodCoverage.
func TestRPCMethodGate_CatchesUnregisteredMethod(t *testing.T) {
	root := rpcMethodGateModuleRoot(t)
	fixtures := []string{
		filepath.Join("internal", "build", "testdata", "seeded-violations", "rpc-method-gate", "cmd", "cascade", "caller.go"),
	}
	violations, unresolved, err := CheckRPCMethodCoverage(root, fixtures)
	if err != nil {
		t.Fatalf("rpc method gate: %v", err)
	}
	if len(unresolved) != 0 {
		t.Fatalf("rpc method gate: fixture call site should resolve to a literal, got unresolved: %v", unresolved)
	}
	if len(violations) != 1 {
		t.Fatalf("rpc method gate: want exactly 1 seeded violation, got %d: %v", len(violations), violations)
	}
	if violations[0].Method != "fixture.unregistered_method" {
		t.Errorf("violation.Method = %q, want %q", violations[0].Method, "fixture.unregistered_method")
	}
}
