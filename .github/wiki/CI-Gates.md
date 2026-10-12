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

## `cascade ci status` aggregates both sources

The gates above are the ones `go test ./internal/build/...` enforces on the
tree. They are a different thing from CI *results*, which cascade records in
the `ci_results` domain from two independent producers:

| Producer | `source` | Written by |
|---|---|---|
| hosted GitHub Actions | `github-actions` | the `cascade-github` plugin's ingestion |
| the local lint/test/build gate | `local` | `cascade ci run` |

`cascade ci status` shows both together, newest first, with the `source`
column naming which produced each row. A row with no recorded source reads
as `github-actions`: every row written before the source marker existed came
from the polling path, so an absent marker can only mean that producer.

Local run ids are additionally allocated in the **negative** integers, per
repository, so they cannot collide with GitHub's own (always positive)
workflow-run ids — the sign alone tells the two apart in `ci_run` and
`ci_job`, even before the source marker is consulted.

Which repositories may use which producer is the never-pay policy's
decision, and it is enforced in both directions: `cascade ci run` refuses a
repository routed to hosted Actions, and the Actions polling path refuses a
repository routed to the local gate. See `docs/cli-reference/ci.md` for the
`[ci.policy]` and `[ci.local]` keys.

## Golden-parity aggregate lane

The `golden-parity` job in `.github/workflows/ci.yml` runs every committed
golden root's owner tests and collects the result into one report. It runs on
`ubuntu-latest`, `macos-15` and `windows-latest` and is required by `ci-gate`.

`internal/goldencheck` only plans and aggregates; it starts no subprocess.
The job runs the rows:

1. `go run ./internal/goldencheck/cmd/goldenlane plan` discovers the roots
   and prints one `<package><TAB><regex><TAB><root>` line per row. It exits 1
   when a golden root has neither an owner row nor an exclusion.
2. For each line the job runs `go test -json -count=1 -run <regex> <package>`
   into `<n>.json` and never stops on a failing row.
3. `goldenlane report --dir <dir> --out report.json` scores every row and
   exits 1 unless all rows pass. The job uploads `report.json` as the
   `golden-parity-report-<runner>` artifact.

### The owner table

`internal/goldencheck/owners.go` holds `Owners`: one row per golden root
(root, package, `-run` regex) and `Excluded`: roots no test reads, each with a
reason. Discovery finds every `testdata/v1-goldens` directory, every
`v1-goldens/migration` sub-root, and the `testdata/goldens/claude-code`
replay directory. A `v1-goldens` directory whose only entry is a registered
`migration` sub-root is a container and needs no row. The four migration
sub-roots share one row shape: `./internal/migration/tripwire` with
`^TestTripwireStable$`.

### Adding a golden root

Add the row to `Owners` in the same change that adds the root. If no test
reads the root, add it to `Excluded` with the reason instead. Use exact test
names in the regex (`^TestName$` or `^(TestA|TestB)$`): the lane fails when a
named test did not run, so a rename cannot silently drop a check.

### Reading the report

Each row lists `run`, `passed`, `failed` and `skipped` counts, `ok`, and a
`reason` when it failed. A row fails on any of: no captured output, zero
`run` events (the regex matched nothing), a failed test, a package-level
failure (build error, panic, timeout), a test that started but never
finished, no passing test (all skipped), or a named test that did not run.
The text summary printed by the report step shows the same rows, one per line.

## Other gates

See `internal/build/*.go` (gofmt-clean, dead-code, test-only usage,
license allowlist, and this file's own bench-budget harness) for every
other gate `go test ./internal/build/...` runs. Each carries its own
seeded-violation fixture under `internal/build/testdata/seeded-violations/`.
