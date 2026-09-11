// SPORT: internal.bench.RegisteredBudgets/ADDED test coverage (P1-E28-W10-S58-T2).
package bench

import (
	"testing"

	"github.com/acamarata/cascade/internal/build"
)

// TestRegisteredBudgetsEmptyCopy asserts RegisteredBudgets returns an
// independent, length-matching copy on the package's shipped (empty)
// table.
func TestRegisteredBudgetsEmptyCopy(t *testing.T) {
	got := RegisteredBudgets()
	if len(got) != len(Budgets) {
		t.Fatalf("RegisteredBudgets() len = %d, want %d", len(got), len(Budgets))
	}
	got["extra"] = build.Budget{Name: "extra"}
	if len(Budgets) != 0 {
		t.Fatalf("Budgets mutated via a copy: len = %d, want 0", len(Budgets))
	}
}
