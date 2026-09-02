package build

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// benchModuleRoot locates the repo root by walking up from this file.
func benchModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("bench gate: runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("bench gate: no go.mod found walking up from %s", file)
		}
		dir = parent
	}
}

// TestAssertBudgets_RealTreeGreen: this ticket ships zero registered
// budgets (§D-38 — consumers register their own later), so the harness run
// with an empty budget map must always be green regardless of results.
func TestAssertBudgets_RealTreeGreen(t *testing.T) {
	results := []BudgetResult{
		{Name: "anything.at.all", NsPerOp: 999999, AllocsPerOp: 999, BytesPerOp: 999999},
	}
	v := AssertBudgets(results, map[string]Budget{})
	if len(v) != 0 {
		t.Fatalf("expected zero violations with an empty budget registry, got %v", v)
	}
}

// TestAssertBudgets_NoOpWithoutMatchingName: a result whose Name has no
// registered budget is skipped, not a violation and not an error.
func TestAssertBudgets_NoOpWithoutMatchingName(t *testing.T) {
	budgets := map[string]Budget{
		"registered.op": {Name: "registered.op", MaxNsPerOp: 10},
	}
	results := []BudgetResult{
		{Name: "unregistered.op", NsPerOp: 99999},
	}
	v := AssertBudgets(results, budgets)
	if len(v) != 0 {
		t.Fatalf("expected zero violations for an unregistered result name, got %v", v)
	}
}

// TestAssertBudgets_SeededViolationRed_Overrun: the seeded fixture pairs a
// budget with a result that exceeds every metric — the harness must flag
// all three.
func TestAssertBudgets_SeededViolationRed_Overrun(t *testing.T) {
	root := benchModuleRoot(t)
	src := filepath.Join(root, "internal", "build", "testdata", "seeded-violations", "budget", "overrun.json")
	budget, result, err := LoadBudgetFixture(src)
	if err != nil {
		t.Fatalf("bench gate: loading fixture %s: %v", src, err)
	}
	if budget.Name == "" || result.Name == "" {
		t.Fatalf("bench gate: fixture decoded empty, check overrun.json shape: budget=%+v result=%+v", budget, result)
	}
	v := AssertBudgets([]BudgetResult{result}, map[string]Budget{budget.Name: budget})
	if len(v) != 3 {
		t.Fatalf("expected 3 violations (ns/op, allocs/op, bytes/op) from seeded overrun fixture, got %d: %v", len(v), v)
	}
}

// TestLoadBudgetFixture_MissingFile: a missing fixture path is a plain
// error, never a panic — the harness must degrade predictably.
func TestLoadBudgetFixture_MissingFile(t *testing.T) {
	_, _, err := LoadBudgetFixture(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err == nil {
		t.Fatal("expected an error loading a missing fixture file, got nil")
	}
}
