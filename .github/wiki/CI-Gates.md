# CI Gates

This page documents the gates enforced by `go test ./internal/build/...` and
by the dedicated `bench` workflow, and where a maintainer registers a new
one.

## Bench budget registration

The bench lane (`.github/workflows/bench.yml`) has two independent steps:

- **benchstat vs base branch** — an observational comparison; it never
  fails the build on its own.
- **budget-assertion harness** (`internal/bench`) — the gate that fails
  the build the moment a measured benchmark result exceeds a registered
  ceiling.

Budget *values* are never invented by the harness or by this wiki page.
Per R-16.72, a numeric ceiling belongs to the subsystem ticket that owns
the benchmark: that ticket states the ceiling in its own acceptance
criteria, and a maintainer transcribes it into the single Go table
`internal/bench/registry.go` (`Budgets`), keyed
`"<import path>/<BenchmarkName>"`. There is no YAML file and no
source-comment marker — the map is the only registration surface.

As of P1-E28-W10-S58-T2 (the terminal wiring ticket for this gate), a
repo-wide search of every ticket's acceptance criteria for a stated
numeric ns/op, allocs/op or bytes/op ceiling found none: the table ships
empty, and the gate is correspondingly green-empty, matching the
precedent A/S-01.T3 set when it built the harness itself. This is the
correct state for an inventory with zero registered entries, not a
shortfall — the gate starts asserting the moment the first entry lands.

### Registering a budget

1. State the ceiling(s) — `MaxNsPerOp`, `MaxAllocsPerOp`, `MaxBytesPerOp`
   — in your ticket's acceptance criteria, with the measurement basis
   (which machine/CI runner, how much headroom over the observed number).
2. Add one entry to `Budgets` in `internal/bench/registry.go`, keyed by
   the benchmark's full import path plus name.
3. `internal/bench.TestBudgetRegistry` asserts the key matches its own
   `Budget.Name` field and the `"<import path>/<BenchmarkName>"` shape;
   run it locally before opening the PR.
4. The bench workflow's budget-assertion step runs the real suite and
   qualifies each result the same way `internal/bench.ParseBenchOutput`
   does, so a registered budget starts being enforced on the very next
   PR without any workflow change.

### Proving the gate can fail

`internal/bench/testdata/seeded-violations/overrun.json` is a permanent
fixture: a fake budget paired with a measured result that exceeds every
metric. `TestBudgetViolationGate` loads it and asserts `Gate` reports all
three violations — the negative proof that removing or weakening the
assertion call makes this test fail. The fixture is never merged into
the live `Budgets` table and is never removed from the tree.

## Other gates

See `internal/build/*.go` (gofmt-clean, dead-code, test-only usage,
license allowlist, and this file's own bench-budget harness) for every
other gate `go test ./internal/build/...` runs. Each carries its own
seeded-violation fixture under `internal/build/testdata/seeded-violations/`.
