// Package build (this file) holds the benchmark budget-assertion HARNESS
// from P1-E01-W1-S01-T3 (06-FORGE-SPEC.md §5.13, restated by
// 12-QUALITY-CONSTITUTION.md §D-38): comparison logic only. This ticket
// registers ZERO numeric budgets — each consuming subsystem ticket owns its
// own Budget values, and AB/S-58.T2 owns collecting and asserting them in
// CI (`.github/workflows/bench.yml` invokes this package's AssertBudgets
// once budgets exist; today it runs with an empty budget list and is
// trivially green).
package build

import (
	"encoding/json"
	"fmt"
	"os"
)

// Budget is a named performance ceiling a consuming subsystem registers
// for one of its benchmarks. Zero fields are "no ceiling on that metric" —
// AssertBudgets skips a metric whose budget is zero.
type Budget struct {
	Name           string
	MaxNsPerOp     float64
	MaxAllocsPerOp int64
	MaxBytesPerOp  int64
}

// BudgetResult is a measured benchmark outcome, in the same shape as
// `go test -bench` reports (ns/op, allocs/op, bytes/op), compared against
// a Budget of the same Name.
type BudgetResult struct {
	Name        string
	NsPerOp     float64
	AllocsPerOp int64
	BytesPerOp  int64
}

// BudgetViolation is one metric of one benchmark result that exceeded its
// registered budget.
type BudgetViolation struct {
	Name   string
	Metric string
	Budget float64
	Actual float64
}

// String renders a violation as a single human-readable line, used by the
// bench.yml CI step and by CLI-side reporting.
func (v BudgetViolation) String() string {
	return fmt.Sprintf("%s: %s budget %.2f exceeded by actual %.2f", v.Name, v.Metric, v.Budget, v.Actual)
}

// AssertBudgets compares each result against budgets[result.Name] and
// returns a violation per metric that exceeds its ceiling. A result whose
// Name has no entry in budgets is skipped — budgets are opt-in per §D-38,
// not implied by a benchmark merely existing. With an empty budgets map
// (this ticket's shipped state) AssertBudgets always returns nil.
func AssertBudgets(results []BudgetResult, budgets map[string]Budget) []BudgetViolation {
	var out []BudgetViolation
	for _, r := range results {
		b, ok := budgets[r.Name]
		if !ok {
			continue
		}
		if b.MaxNsPerOp > 0 && r.NsPerOp > b.MaxNsPerOp {
			out = append(out, BudgetViolation{r.Name, "ns/op", b.MaxNsPerOp, r.NsPerOp})
		}
		if b.MaxAllocsPerOp > 0 && r.AllocsPerOp > b.MaxAllocsPerOp {
			out = append(out, BudgetViolation{r.Name, "allocs/op", float64(b.MaxAllocsPerOp), float64(r.AllocsPerOp)})
		}
		if b.MaxBytesPerOp > 0 && r.BytesPerOp > b.MaxBytesPerOp {
			out = append(out, BudgetViolation{r.Name, "bytes/op", float64(b.MaxBytesPerOp), float64(r.BytesPerOp)})
		}
	}
	return out
}

// budgetFixture is the on-disk shape of a seeded budget/result pair under
// testdata/seeded-violations/budget/ — plain JSON data, read only by tests.
type budgetFixture struct {
	Budget Budget       `json:"budget"`
	Result BudgetResult `json:"result"`
}

// LoadBudgetFixture reads and decodes one budgetFixture JSON file. It never
// writes anything; callers materialize fixtures into t.TempDir() themselves
// per Art.7.1 when a test needs an on-disk copy.
func LoadBudgetFixture(path string) (Budget, BudgetResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Budget{}, BudgetResult{}, err
	}
	var f budgetFixture
	if err := json.Unmarshal(data, &f); err != nil {
		return Budget{}, BudgetResult{}, err
	}
	return f.Budget, f.Result, nil
}
