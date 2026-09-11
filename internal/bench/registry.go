// Package bench is the terminal wiring step for the benchmark
// budget-assertion harness built by A/S-01.T3 (internal/build/bench.go).
// That ticket shipped comparison logic only, deliberately registering
// zero numeric budgets: per 06-FORGE-SPEC §5.13 and R-16.72, budget
// VALUES belong to the subsystem ticket that owns the benchmark, stated
// in that ticket's own acceptance criteria, never invented here.
//
// Purpose: enumerate every budget a consuming subsystem ticket has
// actually registered, expose it as a single Go table (R-16.72: a map,
// never a YAML file or a source-comment marker), and give the bench.yml
// CI step one call that fails the build the moment a measured result
// exceeds its registered ceiling.
//
// Inputs: Budgets, the registration table below, keyed
// "<import path>/<BenchmarkName>" per R-16.72. Entries arrive by editing
// this file when a subsystem ticket's acceptance criteria states a
// numeric ns/op, allocs/op or bytes/op ceiling for one of its benchmarks.
//
// Outputs: RegisteredBudgets returns a defensive copy of the table so a
// caller cannot mutate the registration set at runtime.
//
// Constraints: as of this ticket, a repo-wide search of every ticket's
// acceptance_criteria for a stated numeric bench threshold found none —
// the three Benchmark functions already in-tree (providers/sqlite,
// internal/fleet, internal/conductor) were each added by tickets that
// never stated a ns/op/allocs/op/bytes/op ceiling. Per A/S-01.T3's own
// precedent ("harness is green with zero registered budgets"), an empty
// table is the correct, honestly-reported state, not a shortfall of this
// ticket: this ticket's job is completeness of assertion over whatever
// is registered, and the inventory of registered budgets is complete at
// zero. Adding a budget number for a benchmark whose owning ticket never
// stated one would be inventing a threshold this repo has ruled belongs
// to that ticket alone.
//
// SPORT: internal.bench.Budgets/ADDED (P1-E28-W10-S58-T2).
package bench

import "github.com/acamarata/cascade/internal/build"

// Budgets is the registration table. Key format is fixed by R-16.72:
// "<import path>/<BenchmarkName>", so a lookup never ambiguates two
// same-named benchmarks in different packages. Empty per the Constraints
// note above; the map itself, its key format and RegisteredBudgets are
// the durable contract a later ticket's registration edits against.
var Budgets = map[string]build.Budget{}

// RegisteredBudgets returns a defensive copy of Budgets, so a caller
// (the gate, a test, a future CLI surface) can inspect the registration
// table without being able to mutate the package-level source of truth.
func RegisteredBudgets() map[string]build.Budget {
	out := make(map[string]build.Budget, len(Budgets))
	for k, v := range Budgets {
		out[k] = v
	}
	return out
}
