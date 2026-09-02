# Lint wall and architecture boundary gates

Status: active from Wave 1 (P1-E01-W1-S01-T2). This is a wall, not advice —
`golangci-lint run` and `go test ./internal/build/...` must both be green
with zero findings before any change lands.

## Active linters (`.golangci.yml`, repo root)

The ratified set (06-FORGE-SPEC.md §5.25, R-14.101, PRI hard rule 2):

| Linter | What it enforces |
|---|---|
| `errcheck` | every returned `error` is checked |
| `staticcheck` | correctness/style analysis (SA/ST checks) |
| `govet` | `go vet`'s built-in analyzers |
| `revive` | style/lint rules (default rule set) |
| `exhaustive` | `switch` statements over a defined type must cover every value — Go has no compile-time exhaustiveness check, and `internal/conductor`'s task-class/sensitivity enums (06-FORGE-SPEC.md §5.16) rely on this |
| `depguard` | import-path allow/deny rules, see below |
| `funlen` | functions capped at 50 physical lines (12-QUALITY-CONSTITUTION.md Art.10.3) — the statement-count metric is disabled so only the line cap applies |

`.golangci.yml` targets the golangci-lint v2 config schema (installed:
v2.13.2). Run `golangci-lint config verify` after editing it.

### depguard import-boundary rules

Two rules encode 12-QUALITY-CONSTITUTION.md Art.10.2 directly in the lint
config:

- `plugins-providers-boundary` — files under `plugins/**` and `providers/**`
  may not import `github.com/acamarata/cascade/internal` (or any
  subpackage). They may import `pkg/**` and any stdlib/third-party package.
- `pkg-no-internal` — files under `pkg/**` (the public SDK surface) may
  never import `internal/**` either.

These are a lint-time check. The same two boundaries — plus the two rules
depguard cannot express (`cmd/**`-only composition, no import cycles) — are
also enforced independently at the Go-test level; see below.

## Architecture and size gates (`internal/build/`)

`internal/build` holds only `_test.go` files — it ships no code, it is pure
build-time verification. `go test ./internal/build/...` runs every gate.

| File | Gate |
|---|---|
| `arch_test.go` | import-boundary rules (plugins/providers → pkg only; pkg never imports internal; cmd/** is the sole composition root) and no-import-cycles, as an independent Go-level check alongside depguard |
| `filecap_test.go` | 300-line file cap (Art.10.3) over every `.go` file in the tree, `testdata/` excluded |
| `desktop_test.go` | no GUI-toolkit import anywhere under `internal/`, and no platform-only import (e.g. `golang.org/x/sys/windows`) outside a build-tagged file — the core is headless and product-agnostic (ASI Policy 2, PRI hard rule 1) |
| `boundary_test.go` | (P1-E01-W1-S01-T7) pkg/cmd boundaries return `pkg/cascade` taxonomy errors, never a raw `fmt.Errorf`/`errors.New` |

Each gate scans both:

1. **The real tree** — must be clean (`TestXxx_RealTreeGreen`).
2. **Its own seeded-violation fixture** under
   `internal/build/testdata/seeded-violations/<rule>/` — must trip the gate
   (`TestXxx_SeededViolationRed`). This proves the gate actually detects
   what it claims to, not just that it never fires.

## How the seeded-violation fixtures work

Fixtures are plain data, never shipped code:

- They live under `internal/build/testdata/seeded-violations/`. Go's
  toolchain never compiles a `testdata/` directory into any build, so a
  fixture importing a forbidden package or exceeding the line cap can never
  reach a shipped binary (12-QUALITY-CONSTITUTION.md Art.1).
- Each `..._SeededViolationRed` test copies its fixture tree into a fresh
  `t.TempDir()` before scanning it (12-QUALITY-CONSTITUTION.md Art.7.1:
  tests write only under `t.TempDir()`, never the repo or `$HOME`). The
  scanners themselves never need to `go build` or resolve the fixture's
  imports — they parse import declarations directly with `go/parser`, so a
  fixture's imports can name paths that don't actually exist anywhere.

## Running the wall locally

```sh
# Full lint wall (must report "0 issues")
golangci-lint run

# Just this package (fast path while iterating)
golangci-lint run ./internal/build/...

# Architecture/size/lint gates
go test ./internal/build/...

# Sanity checks the wall assumes stay green
go build ./...
go vet ./...
```

CI wiring for all of the above is P1-E01-W1-S01-T3 (depends on this
ticket); everything here already runs locally, before any CI exists.
