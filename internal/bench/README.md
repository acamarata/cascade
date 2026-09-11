# internal/bench

Terminal wiring for the benchmark budget-assertion harness A/S-01.T3
built (`internal/build/bench.go`). This package owns the registration
table and the CI gate; the comparison logic itself stays in
`internal/build` per that ticket.

## Registration protocol

Per R-16.72: budgets are a single Go table, `Budgets` in `registry.go`,
keyed `"<import path>/<BenchmarkName>"`. There is no YAML file and no
source-comment marker. A subsystem ticket that owns a benchmark states
its ceiling(s) in its own acceptance criteria; a maintainer transcribes
that into one `Budgets` entry.

As of this package landing (P1-E28-W10-S58-T2), the table is empty: no
ticket in the current tree has stated a numeric ns/op, allocs/op or
bytes/op ceiling for any of its benchmarks. This is the correctly
reported inventory state, not a shortfall — see `TestBudgetRegistry`'s
doc comment and `.github/wiki/CI-Gates.md`.

## What's here

- `registry.go` — the `Budgets` table and `RegisteredBudgets()`.
- `gate.go` — `ParseBenchOutput` (qualifies each `go test -bench`
  result's name with its owning import path) and `Gate` (runs results
  through `internal/build.AssertBudgets`).
- `testdata/seeded-violations/overrun.json` — a permanent fixture
  proving the gate turns red; never merged into `Budgets`.

## CI wiring

`.github/workflows/bench.yml`'s budget-assertion harness job runs this
package's `TestBudgetRegistry`, `TestBudgetViolationGate` and
`TestBudgetGateOnHEAD` directly. There is no separate CLI entry point in
this ticket's scope; `internal/build/testonly-allow.json` carries the
three exported symbols (`Gate`, `ParseBenchOutput`, `RegisteredBudgets`)
with `expected_caller` pointing at a future `cascade bench` CLI surface
that would call them from non-test code.
