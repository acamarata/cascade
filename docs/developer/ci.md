# CI lanes and supply-chain gates

Status: active from Wave 1 (P1-E01-W1-S01-T3). This documents the four CI
workflows under `.github/workflows/` and the gate logic they invoke, per
06-FORGE-SPEC.md §Wave 1 §Epic A T3 and 12-QUALITY-CONSTITUTION.md Art.3/
Art.5/Art.7.4/Art.10.1. This repo is PUBLIC: every lane runs on
GitHub-hosted runners, free (ASI Policy 8: never pay for GitHub Actions,
never self-host what GitHub gives free).

## Lane map

| Workflow | Job | Triggers | What it gates |
|---|---|---|---|
| `ci.yml` | `build-test` | push, PR | `go build`/`go vet`/`go test` on each of darwin/arm64, darwin/amd64, linux/amd64, linux/arm64, windows/amd64, each on its OWN native runner (Art.5.1: every platform compiles AND runs its suite, never cross-compiled-and-assumed) |
| `ci.yml` | `race` | push, PR | `go test -race ./...` |
| `ci.yml` | `lint` | push, PR | `golangci-lint run` (the wall, see `lint-wall.md`) + `go test ./internal/build/...` (A/S-01.T2's arch/filecap/desktop/boundary gates plus this ticket's license/bench gates) |
| `ci.yml` | `ci-gate` | push, PR | aggregate: fails if `build-test`, `race`, or `lint` is red; this is the required merge status every later ticket's "checks green in CI" (Art.3) hangs off |
| `supply-chain.yml` | `govulncheck` | push, PR | `govulncheck ./...`, known-vulnerability scan |
| `supply-chain.yml` | `licenses` | push, PR | `internal/build`'s license-allowlist gate (`go test ./internal/build/... -run TestLicenses`), plus the real `go-licenses` tool as an independent second classifier |
| `supply-chain.yml` | `dependency-review` | PR only | GitHub's advisory-database diff on any go.mod/go.sum change |
| `fuzz-nightly.yml` | `fuzz-corpus-location` | nightly cron, manual | every `testdata/fuzz` directory in the tree lives under `internal/testdata/fuzz/`, never repo root (06 §5.7, Art.10.1) |
| `fuzz-nightly.yml` | `fuzz` | nightly cron, manual | discovers and runs every `FuzzXxx` target that exists (none yet, green-empty by design, see below) |
| `bench.yml` | `budget-harness` | PR, manual | `internal/build`'s `AssertBudgets` harness (`go test ./internal/build/... -run TestAssertBudgets`); zero budgets registered by this ticket, green-empty by design |
| `bench.yml` | `benchstat` | PR, manual | `benchstat` comparison of the PR head vs its base branch, observational until a later ticket registers budgets |

## Why the matrix uses native runners, not cross-compilation

Art.5.1 requires every supported platform to both COMPILE and RUN its test
suite in CI, on every push: "builds on macOS, assumed fine elsewhere" is
exactly what it forbids. GitHub provides free, native hosted runners for
all five target platforms on public repos: `macos-14` (darwin/arm64),
`macos-13` (darwin/amd64), `ubuntu-latest` (linux/amd64),
`ubuntu-24.04-arm` (linux/arm64), `windows-latest` (windows/amd64). Using
one native runner per platform means every `go test` invocation executes
real binaries, sidestepping the "cannot execute a cross-compiled binary"
limitation the interactive dev environment has on darwin.

## Green-empty lanes are not stubs

The fuzz and bench lanes both currently run against zero real targets
(zero `FuzzXxx` functions, zero registered budgets). This is deliberate
infrastructure-first sequencing (§D-38, restated by 06 §5.13): this ticket
ships the lane and the harness; parser/decoder tickets add `FuzzXxx`
targets, and each consuming subsystem ticket registers its own budgets
(asserted for real by AB/S-58.T2). A green-empty run is the intended state
until those land; it is infrastructure whose consumers arrive by design,
not a simulated capability (12-QUALITY-CONSTITUTION.md Art.1 distinguishes
the two explicitly).

## The license allowlist gate

`internal/build/licenses.go` implements the allowlist purely in Go: no
extra module dependency (R-14.115 forbids a concurrent dependency-adding
ticket) and no extra tool install, so `go test ./internal/build/... -run
TestLicenses` gives the identical result locally and in CI. It:

1. Parses `go.mod`'s require directives (a small hand-rolled parser;
   `golang.org/x/mod/modfile` would itself be a new dependency).
2. Looks each module path up in `KnownModuleLicenses`, a maintained
   registry mapped to a verified SPDX identifier.
3. Fails any module missing from the registry ("unknown license", fails
   closed) or whose registered license is not on `LicenseAllowlist`
   (MIT, BSD-2-Clause, BSD-3-Clause, Apache-2.0, ISC, CC0-1.0,
   `.claude/rules/dependency-rules.md` #2).

**Update `KnownModuleLicenses` in the same change that adds or upgrades a
`require` line in go.mod.** The CI `licenses` job also runs the real
`go-licenses` tool as a second, independent layer that classifies each
dependency straight from its LICENSE file text; this catches any drift
between the registry and reality that a stale registry entry could hide.

## The budget-assertion harness

`internal/build/bench.go` ships `Budget`, `BudgetResult`, and
`AssertBudgets(results, budgets)`, pure comparison logic, no global
mutable registry. A consuming subsystem ticket defines its own `Budget`
values and passes them (with its measured `BudgetResult`s) to
`AssertBudgets`; AB/S-58.T2 owns collecting every subsystem's budgets and
asserting the full set in CI. `bench.yml`'s `budget-harness` job runs
`go test ./internal/build/... -run TestAssertBudgets`, which is green with
today's empty budget set and will stay the same command once real budgets
exist elsewhere in the tree.

## The fuzz corpus location gate

Hand-seeded fuzz corpora belong at `internal/testdata/fuzz/<FuzzName>/`
(06-FORGE-SPEC.md §5.7), never at repo root, never scattered elsewhere.
`fuzz-nightly.yml`'s `fuzz-corpus-location` job runs a `find` check for any
`testdata/fuzz` directory outside that home, proven both ways every run:
clean on the real tree, and tripped by the seeded fixture at
`internal/build/testdata/seeded-violations/fuzz-corpus/` (a deliberately
mislocated corpus dir under `pkg/somepkg/testdata/fuzz/`).

## SHA-pinning and Dependabot

Every `uses:` across all four workflows is pinned to a full 40-character
commit SHA, never a floating tag: the tag/version is recorded in a
trailing comment for legibility (e.g. `actions/checkout@3d3c42e... # v7.0.1`).
`.github/dependabot.yml` watches both the `gomod` and `github-actions`
ecosystems weekly; Dependabot understands SHA-pinned `uses:` lines
natively and bumps the SHA and its comment together.

## Running the same gates locally

```sh
# Build + test (this host's native platform only)
go build ./...
go vet ./...
go test ./...
go test -race ./...

# Lint wall + internal/build gates (arch/filecap/desktop/boundary/licenses/bench)
golangci-lint run
go test ./internal/build/...

# License gate only
go test ./internal/build/... -run TestLicenses -v

# Budget harness only
go test ./internal/build/... -run TestAssertBudgets -v

# govulncheck (install once: `go install golang.org/x/vuln/cmd/govulncheck@latest`)
govulncheck ./...
```

`go`/`git` are wrapped by `rtk` in this dev environment, which prints its
own summary but returns an exit code that does not track the real result
(R-14.114). Every command above must be run as `rtk proxy go ...` in this
environment specifically; CI runners have no such wrapper and invoke the
toolchain directly (Art.7.4).
