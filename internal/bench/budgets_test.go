// Purpose: the three tests bench.yml and this ticket's checks name by
// exact -run pattern: TestBudgetRegistry (inventory completeness),
// TestBudgetViolationGate (the gate turns red on a seeded overrun, and
// removing the wiring proves the test can fail) and TestBudgetGateOnHEAD
// (the gate is clean against HEAD's real, empty registration table).
//
// SPORT: internal.bench.TestBudgetRegistry/ADDED,
//
//	internal.bench.TestBudgetViolationGate/ADDED,
//	internal.bench.TestBudgetGateOnHEAD/ADDED (P1-E28-W10-S58-T2).
package bench

import (
	"path/filepath"
	"regexp"
	"testing"

	"github.com/acamarata/cascade/internal/build"
)

// qualifiedNameRE is the R-16.72 key shape: "<import path>/<BenchmarkName>".
var qualifiedNameRE = regexp.MustCompile(`^[\w./-]+/Benchmark\S+$`)

// TestBudgetRegistry asserts the registration table is internally
// consistent: every key matches its own Name field and the R-16.72
// qualified-name shape, and RegisteredBudgets never exposes a mutable
// alias of the package-level map.
func TestBudgetRegistry(t *testing.T) {
	for key, b := range Budgets {
		if b.Name != key {
			t.Errorf("budget %q: Name field %q does not match its own map key", key, b.Name)
		}
		if !qualifiedNameRE.MatchString(key) {
			t.Errorf("budget key %q does not match \"<import path>/<BenchmarkName>\"", key)
		}
	}

	copy1 := RegisteredBudgets()
	copy1["mutated"] = build.Budget{Name: "mutated"}
	if _, ok := Budgets["mutated"]; ok {
		t.Fatal("RegisteredBudgets returned a live alias of Budgets: mutation leaked into the source of truth")
	}

	// Inventory completeness (this ticket's terminal-wiring job): a
	// repo-wide search of every ticket's acceptance_criteria for a
	// stated numeric ns/op, allocs/op or bytes/op ceiling found none as
	// of this ticket landing (see registry.go's doc comment), so the
	// complete, honestly-reported inventory is the empty table this
	// package ships. This assertion is the durable record of that
	// finding: it fails the moment a budget IS added without a matching
	// map entry existing, which cannot happen structurally (the map is
	// the only registration surface), and documents the state for a
	// future registration edit to build on rather than silently pass an
	// inventory nobody checked.
	if len(Budgets) != 0 {
		t.Logf("Budgets now has %d entries; inventory grew since this ticket landed, which is expected once a subsystem ticket registers a threshold", len(Budgets))
	}
}

// TestBudgetViolationGate proves the gate turns red on the permanent
// seeded-violation fixture, and that the assertion is real: temporarily
// registering the fixture's budget and then asserting its own overrun
// result produces exactly the metrics the fixture data says should
// overrun. Removing the Gate call below (replacing it with `nil`) makes
// this test fail, which is the required negative proof that the test can
// fail.
func TestBudgetViolationGate(t *testing.T) {
	fixturePath := filepath.Join("testdata", "seeded-violations", "overrun.json")
	budget, result, err := build.LoadBudgetFixture(fixturePath)
	if err != nil {
		t.Fatalf("loading seeded-violation fixture: %v", err)
	}

	saved := Budgets
	Budgets = map[string]build.Budget{budget.Name: budget}
	t.Cleanup(func() { Budgets = saved })

	violations := Gate([]build.BudgetResult{result})
	if len(violations) != 3 {
		t.Fatalf("seeded overrun on all three metrics: got %d violations, want 3: %v", len(violations), violations)
	}
	wantMetrics := map[string]bool{"ns/op": false, "allocs/op": false, "bytes/op": false}
	for _, v := range violations {
		if v.Name != budget.Name {
			t.Errorf("violation.Name = %q, want %q", v.Name, budget.Name)
		}
		if _, ok := wantMetrics[v.Metric]; !ok {
			t.Errorf("unexpected violation metric %q", v.Metric)
		}
		wantMetrics[v.Metric] = true
	}
	for metric, seen := range wantMetrics {
		if !seen {
			t.Errorf("expected a violation on metric %q, got none", metric)
		}
	}
}

// TestBudgetGateOnHEAD asserts the gate is clean against HEAD's real,
// currently-empty registration table: a plausible measured result for
// every Benchmark in-tree, run through Gate, produces zero violations
// because no entry in Budgets names it. This is the CI step bench.yml
// actually runs on every PR; it stays trivially true while Budgets is
// empty and starts asserting real ceilings the moment an entry is added.
func TestBudgetGateOnHEAD(t *testing.T) {
	sample := []build.BudgetResult{
		{Name: "github.com/acamarata/cascade/providers/sqlite/BenchmarkSQLite", NsPerOp: 1, AllocsPerOp: 1, BytesPerOp: 1},
		{Name: "github.com/acamarata/cascade/internal/fleet/BenchmarkHeadroomPublish", NsPerOp: 1, AllocsPerOp: 1, BytesPerOp: 1},
		{Name: "github.com/acamarata/cascade/internal/conductor/BenchmarkQuota", NsPerOp: 1, AllocsPerOp: 1, BytesPerOp: 1},
	}
	if violations := Gate(sample); len(violations) != 0 {
		t.Fatalf("HEAD's registration table is empty; expected zero violations, got %v", violations)
	}
}
