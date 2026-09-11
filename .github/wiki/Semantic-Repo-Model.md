# Semantic repo model: repository inventory

`internal/repo` builds the deterministic, input-side half of the semantic
repo model (R-16.17): a repository's languages, build/test/lint commands,
layout, CI presence, and existing AI-harness files, all derived from
evidence files on disk by six deterministic detectors. There is no LLM
anywhere in this layer -- inferred, non-deterministic facts are a separate
ledger built by a later ticket.

## Repository identity

Repository identity (a stable id, the git remote URL, and a path hash) is
owned by `internal/context/scope`, not by this package: the
`context_repository` and `context_repo_path` tables (R-16.3) already carry
that shape. `internal/repo` reads and, when a repository has not been seen
before, registers those rows through `scope.GraphStore`; it defines no
parallel repository table of its own.

## The six detector families

Each family's `Detector.Detect(ctx, root)` looks for one evidence marker
and either returns a clean absence (`Detected: false`, no error -- the
marker simply is not there) or a typed error (the marker is present but
malformed). A detector never guesses a language from anything other than
its own evidence file, and a malformed manifest is never silently
reclassified as another family or dropped.

| Family  | Evidence                                   | Default build / test / lint                              |
|---------|---------------------------------------------|------------------------------------------------------------|
| go      | `go.mod`                                     | `go build ./...` / `go test ./...` / `golangci-lint run`   |
| js/ts   | `package.json` (+ lockfile; `pnpm-lock.yaml` selects pnpm) | `pnpm build` / `pnpm test` / `pnpm lint`     |
| rust    | `Cargo.toml`                                 | `cargo build` / `cargo test` / `cargo clippy`               |
| python  | `pyproject.toml` or `requirements.txt`       | `python -m build` / `pytest` / `ruff check`                 |
| swift   | `Package.swift` or `*.xcodeproj/project.pbxproj` | `swift build` / `swift test` / `swiftlint`               |
| generic | (fallback; reads Makefile targets + README presence) | empty unless a Makefile target exists           |

A tree with no language-specific evidence always falls through to the
generic family, which always reports `Detected: true` -- "generic" is
itself the fact, not an absence. When a repository's own `Makefile`
defines a `build`, `test`, or `lint` target, that target overrides the
per-family default command (R-16.37), for every family except generic
(whose own commands are already Makefile-derived).

## Repo-level facts

`ScanLayout` walks the tree once, bounded in depth and entry count, never
following a symlink (stopping both symlink loops and a symlink escaping
the repository root), and reports any repo-relative paths that collide
case-insensitively. `ScanCI` and `ScanHarness` report which CI system
markers (`.github/workflows`, `.gitlab-ci.yml`, `.circleci/config.yml`)
and pre-existing harness files (`CLAUDE.md`, `AGENTS.md`, `.claude/`) exist
at the repository root.

## Membership

Product/workspace membership is resolved entirely through the existing
scope graph (`scope.GraphStore.ParentScopes`, walking the `project` node's
parent chain per R-16.3's resolution order). `internal/repo` stores the
resolved membership refs as read-only evidence; it never writes a
`scope`/`scope_edge` row itself.

## Storage

`Inventory` persists as one additional table,
`context_repo_inventory`, in the existing `context` domain --
`internal/storage/domains.go`'s twelve-domain enumeration stays CLOSED per
R-21.22, so this ticket adds a table to a domain internal/context/scope
already owns rather than a new `DomainID`. See this ticket's journal for
the full contract/tree contradiction this resolves.

## Determinism boundary

Everything in this package is deterministic: two scans of an unchanged
tree (and an unchanged scope graph) produce byte-identical inventories
apart from the scan timestamp, which comes from an injected clock, never
a bare `time.Now()`. Inferred, non-deterministic facts (an LLM's read of
what a repository is "for") are explicitly out of scope here and belong
to the S-67.T2 ledger.

## Symbol/dependency graph

`SymbolGraph`/`GraphNode`/`GraphEdge` (`internal/repo/graph.go`) model a
repository's symbols (packages, exported types/funcs/vars) and the
`declares`/`imports`/`calls` relationships between them. `GraphExtractor`
is a per-language registry (`RegisteredLanguages`/`ExtractorFor`)
mirroring the detector family's own shape; Go, via
`golang.org/x/tools/go/packages`, is the ONLY shipped implementation --
no js/ts/rust/python/swift extractor exists or is claimed anywhere.
`GraphStore` (`graph_store.go`) persists a graph keyed by repository and
scan generation; a rescan replaces the prior graph atomically.

The graph reaches recall as a `graph` corpus type (`CorpusIDGraph`,
registered in `internal/retrieval/corpus/registry.go`; the serializer
lives in `internal/retrieval/graphcorpus.go`, not `corpus/graph.go`,
because `corpus -> retrieval -> fusion -> corpus` is a real import cycle
the code-corpus ticket already worked around). Each node/edge becomes one
provenance-carrying text record upserted through the EXISTING FTS5/vector
paths -- no new chunker, no new store schema -- and inherits the trusted,
local-only-by-default trust tier the code corpus already uses.

### Cross-scope edge gating (R-21.190)

A symbol/dependency edge is inherently cross-repository: repository A
importing repository B's package is a real edge. Every such edge is
stored -- extraction never drops one -- but a live traversal may cross it
only when the `context/scope` graph holds a `depends_on` or
`shares_context_with` edge between the two scopes. `CrossScopeGate`
(`graph_scope.go`) answers that permission check against the real
`scope.GraphStore`, and `GatedWalk` applies it at EVERY hop of a bounded,
cycle-safe walk: a denied hop is recorded as a `Refusal` and that branch
stops there, never post-rank and never silently dropped. At the
already-ingested-corpus-record layer, no new mechanism is needed at all:
a `graph` record is an ordinary `corpus.Record` with a `scope_ref`, so
the EXISTING `Store.Query`/`Membership` scope check already denies a
cross-scope `graph` record the same way it denies a cross-scope `code`
record -- proved by the `graph-leg` extension of the shared-vocabulary
leak fixture (`internal/retrieval/corpus/testdata/scope-leak/graph-leg/`).

### Symbol reachability (R-21.182)

`Reachability`/`Reachable` (`reachability.go`) answers the query
AC/S-59.T4's change-footprint rule unions with its pre/post-image paths:
given a set of changed file paths, which packages are reachable through
graph edges into a package carrying an `auth`, `secret` or `schema`
`SensitiveClass` (a directory-prefix derivation this ticket owns,
mirroring `internal/jobs/risk.go`'s own disclosed Critical-floor
derivation). A path this graph resolves to nothing returns a typed
`KindNotFound` error, distinct from a resolved path with nothing sensitive
reachable (an empty, successful result) -- the two are never conflated.
A cycle in the input graph cannot loop the walk: a visited-set guard is
tested against a real cycle even though a Go import graph is acyclic at
package level.

At the daemon composition root, `Manifest.RegisterReachability`
(`internal/daemon/subsystems.go`) adapts `Reachable` into the
`internal/jobs.ReachabilityFn` seam R-21.257 settles
(`func(ctx, paths) ([]string, error)`, fixing the class set to
auth/secret/schema): the closure is the only adapter, `internal/jobs`
never imports this package, and `Reachable` keeps its own `classes`
parameter and error return. No daemon startup path calls
`RegisterReachability` yet -- the scheduler/footprint-rule composition is
a later ticket's, matching `RegisterConductorRouter`'s own precedent in
the same file.
