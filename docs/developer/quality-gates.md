# Quality Constitution gates

Status: active from Wave 1 (P1-E01-W1-S01-T8). This document is the
executable-gate companion to `.claude/planning/p1/12-QUALITY-CONSTITUTION.md`
(not tracked in this public repo — see the plan's own docs for readers with
access to it): every article that names a CI-enforced gate is implemented
in `internal/build/`, proven RED on a seeded-violation fixture and GREEN on
the real tree, and wired into `.github/workflows/ci.yml`. This complements
`docs/developer/lint-wall.md` (A-T2's architecture/size/boundary gates) and
`docs/developer/hygiene.md` (A-T5's commit + identifier-sweep gates) — read
those first for the sibling gates this document does not repeat.

## Gate index

| Gate | Article | Source | Fixture |
|---|---|---|---|
| Stub gate | Art.1.2 | `stubgate.go` | `testdata/seeded-violations/stubgate/` |
| Clean-root gate | Art.10.1 | `cleanroot.go` | `testdata/seeded-violations/cleanroot/` |
| AST clock/rand gate | Art.7.3 (R-14.132) | `clockgate.go` | `testdata/seeded-violations/clockgate/` |
| Coverage floor + ratchet | Art.4 | `coveragegate.go` | `testdata/seeded-violations/coverage/` |
| No-sleep lint | Art.7.3 | `hygiene.go` | `testdata/seeded-violations/hygiene/` |
| No-network unit lane | Art.7.2 | `hygiene.go` | `testdata/seeded-violations/hygiene/` |
| Redirected-HOME + clean-tree | Art.7.1 | `hygiene.go` | `testdata/seeded-violations/hygiene/` |
| Dead-code gate | Art.10.5 | `deadcode.go`, `deadcode_scan.go` | `testdata/seeded-violations/deadcode/` |
| Godoc + Example gate | Art.10.6 | `deadcode_godoc.go` | `testdata/seeded-violations/godoc/` |
| Flake quarantine registry | Art.7.5 / Art.6 | `flakes.go` | `testdata/seeded-violations/flakes/`, `flake-registry.json` |

Every gate follows the established pattern (`docs/developer/lint-wall.md`):
pure scanning/parsing logic in a non-test `.go` file, `_test.go` walks the
real tree (asserting GREEN) and the seeded fixtures (asserting RED), and
fixtures materialize under `t.TempDir()` where the check writes anything
(Art.7.1).

## Stub gate (Art.1.2)

Denies, in shipped (non-`_test.go`) files under `cmd/`, `internal/`,
`pkg/`, `providers/`, `plugins/`: a directive-style `TODO`/`FIXME`/`XXX`
comment (the marker word must START the comment body — a sentence that
merely mentions the word does not fire, closing a real false-positive this
gate's own package doc and `plugins/examples/example-builtin/plugin.go`
tripped during development), a string literal containing "not implemented"
or "unimplemented" (AST-matched, not a raw text scan — comments discussing
the rule are invisible to this path by construction), a
`Mock`/`Fake`/`Noop`-prefixed type declaration, or a `return nil, nil`
line trailed by a placeholder marker comment.

**Escape:** `// CASCADE-ALLOW: <ticket-id> <reason>` on the offending line
or the line above it. The ticket-id must match this plan's contract-id
grammar (`P<n>-<Epic><n>-W<n>-S<n>-T<n>`) and the reason must be non-empty.
Art.1.2 also requires "an open ticket" — this repo's ticket state
(`.claude/planning/p1/phase/**`) is gitignored (PRI hard rule 3), so a
public-repo CI checkout has no ticket database to query. The gate
therefore verifies the comment's SYNTACTIC shape only; verifying the
referenced ticket is actually open is a CR-B/review-time process control,
not something this Go-level gate can prove. This limitation is recorded
here rather than silently overclaimed.

## Clean-root gate (Art.10.1)

Scans `git ls-files` (never a raw filesystem walk — a gitignored build
artifact like the local `/cascade` binary must never be flagged) for the
repo root's tracked entries and fails on anything outside the
RELEASE-STATE allowlist: `README.md`, `LICENSE`, `CHANGELOG.md`,
`.gitignore`, `install.sh`, `go.mod`, `go.sum`, `.golangci.yml`,
`.goreleaser.yaml`, `Makefile`, and the directories `.github/ cmd/
internal/ pkg/ providers/ plugins/ apps/ docs/ testdata/`. This is the
RELEASE-STATE set (09-REVIEW-RESOLUTIONS.md §Round 7), not today's Wave-1
tree — `CHANGELOG.md`, `install.sh`, and `Makefile` are on the allowlist
despite not existing yet; their absence is never a violation, only an
extra untracked path is.

## AST clock/rand gate (R-14.132)

`forbidigo` (`.golangci.yml`) matches selector TEXT, so `import t "time"`
then `t.Now()` evades it completely, and a dot-import evades it too. This
gate resolves each file's import aliases before matching (the same
technique `internal/build/boundary_test.go` uses for the raw-error
boundary rule) and additionally rejects a dot-import of `time` or
`math/rand` outright. Denied set mirrors `.golangci.yml`'s forbidigo list
exactly (`time.Now`, `time.Since`, the seeded/unseeded `math/rand` draw
functions) — it deliberately does NOT add `time.Tick`/`After`/`NewTimer`/
`Unix`, a separate forbidigo gap R-14.132 named but did not assign here.
Exempt: `_test.go` files and the two canonical Clock implementations
(`internal/runtime/clock.go`, `internal/testkit/clock.go`).

## Coverage floor + ratchet (Art.4)

Consumes the `coverage` job's profile (`internal/build/coveragegate.go`
parses the raw Go coverage-profile format directly, aggregating statement
coverage per package — no dependency on `go/packages` or a new module).
**Statements only**: Go's stdlib coverage tooling has no branch-coverage
mode, and this repo has adopted no separate branch-coverage tool, so the
Branches column in Art.4's table is a named, honest gap, not silently
claimed. Tiers are assigned by directory: `internal/{policy,secrets,audit}`
= security (≥90%), `cmd/**` = CLI (≥70%), `providers/**` + `plugins/**` =
plugins (≥80%), everything else under `internal/**` = core (≥85%). `pkg/**`
is excluded from Art.4 floors entirely — it is the public SDK contract
surface (often zero-statement by construction: an interface has no
executable body), governed instead by the dead-code and godoc gates below.
The ratchet compares each package's measured coverage against
`internal/build/testdata/coverage-baseline.json` and fails on ANY drop,
even above the floor. Live check: `TestCoverageGate_Live`, gated by
`CASCADE_COVERAGE_PROFILE` (a nested `go test ./...` inside a running
`go test` process was probed directly during development and hangs on
build-cache lock contention past 5 minutes — this is why the check is
env-var-gated against an already-produced profile, exactly like the
hygiene gates below, rather than running the suite itself).

## Test hygiene (Art.7)

- **No-sleep lint** (Art.7.3): AST-resolved `time.Sleep` scan (same alias
  technique as the clock gate) across shipped code. Allowlisted:
  `internal/sync/**` (declared, not yet populated) and one individually
  justified pre-existing file outside this ticket's scope,
  `internal/storage/storetest/queue_suite.go` (a hard-capped, documented
  poll loop) — see `hygiene.go`'s package doc for the full reasoning; that
  file's owner should adopt a `CASCADE-ALLOW` comment in-file once it is
  next in scope, at which point the hardcoded entry should be removed.
- **No-network unit lane** (Art.7.2): an untagged `_test.go` file may not
  import `net` or `net/http` — a REAL, provable rule that is intentionally
  stricter than Art.7.2's literal text (an `httptest`-only unit test would
  also need the `integration` build tag) because a precise "is this call a
  real outbound request or a local httptest server" static check cannot be
  proven correct by inspection. No file in the repo trips this today; a
  future httptest-based unit test revisits this trade-off, named here
  rather than hidden behind a fragile heuristic.
- **Redirected-HOME + clean-tree** (Art.7.1): `HomeDirEntries` and
  `GitStatusPorcelain`/`AssertGitStatusUnchanged` are pure assertion
  primitives; the actual suite run happens in ci.yml's `redirected-home`
  job (HOME redirected to an empty dir, `git status --porcelain` snapshot
  taken before and after), and `TestHygieneEnvironment_Live` (gated by
  `CASCADE_HYGIENE_HOME_DIR`/`CASCADE_HYGIENE_GIT_BEFORE_FILE`/
  `CASCADE_HYGIENE_GIT_AFTER_FILE`) asserts the redirected HOME is still
  empty and the two snapshots match.

## Dead-code gate (Art.10.5)

Cross-file, cross-package AST scan: a symbol is "used" if its bare
identifier (same package) or a cross-package qualified selector (alias
resolved) appears ANYWHERE in the scanned tree — including its own
declaring file, including `_test.go` files — EXCLUDING the identifier
occurrence that IS its own declaration. An earlier draft excluded only
same-FILE occurrences and immediately false-positived on the most common
Go idiom there is (`v := Fn()` never re-spells the return type `Fn`
returns); the position-based fix dropped the false-positive count from 16
in `internal/build` alone to zero, confirmed by direct probe. This is
still not full type-aware analysis (that needs `go/types` + import
resolution, effectively `golang.org/x/tools/go/packages` — a dependency
this ticket is not authorized to add, R-14.115): a symbol reached only
through struct embedding without ever naming its type can still evade it.
`internal/**` unused exported symbols hard-fail CI. `pkg/**` unused
symbols are exempt if their godoc contains the literal marker
`SDK-INTENT` (a deliberate, forward-declared public API note); otherwise
they fail too.

## Godoc + Example gate (Art.10.6)

Every exported top-level `func`/`type`/`const`/`var` in `pkg/**` must carry
a preceding doc comment. Separately, every `pkg/**` subpackage that
declares at least one exported top-level function must ship at least one
`Example*` function in a `_test.go` file (scoped per-package, not per
symbol — this repo's existing `pkg/cascade`, `pkg/plugin`, `pkg/provider`
each already ship one or more).

## Flake quarantine registry (Art.7.5 / Art.6)

`internal/build/flake-registry.json` (currently `[]` — no test has ever
flaked in this repo) is a REGISTRY, not a flake detector: nothing in this
gate re-runs the suite hunting for flakiness. `ValidateFlakeRegistry`
requires every entry to name the exact test (`<import path>.<TestName>`),
an owning ticket matching the plan's id grammar, a non-empty reason, and a
non-past `YYYY-MM-DD` expiry — an EXPIRED entry is itself a violation, the
"retried away forever" failure Art.7.5 exists to prevent. `IsQuarantined`
lets a future test runner consult the registry before treating a failure
as blocking.

## Verifying locally

```sh
go test ./internal/build/...            # every gate's real-tree + seeded proofs
go vet ./internal/build/...
golangci-lint run --allow-parallel-runners internal/build/...
```

The two live checks (`TestCoverageGate_Live`, `TestHygieneEnvironment_Live`)
skip locally unless their env vars are set — see the CI job comments in
`.github/workflows/ci.yml` (`coverage-gate`, `redirected-home`) for exactly
how to reproduce them.
