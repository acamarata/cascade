# Graph-leg scope-leak fixture (R-21.190)

## What this is

The graph-corpus extension of the sibling `scope-leak/` fixture, for the
same leak property applied to `graph` corpus records rather than `code`
records: a session scoped to `project:scope-a` never receives a
`project:scope-b` graph fact record while no relationship edge is
declared between the two scopes, and receives it only once a
`depends_on` or `shares_context_with` edge is declared.

Each record is one provenance-carrying graph fact, in the shape HOW-4 of
P1-E33-W7-S67-T3 (`internal/repo/graph.go`'s `GraphNode`/`GraphEdge`
serialized to one short descriptive text record) upserts through: e.g.
`scope-a/graph#app.Run-imports-policy.Check`. The record id deliberately
names a symbol ("billing.Charge") that would read as sensitive if it
leaked, so a reviewer scanning a failing test's output sees exactly what
crossed the boundary.

## Why no new mechanism is exercised here

R-21.190's own HOW text requires the graph corpus type to reuse the
EXISTING F/S-10.T2/T3 upsert paths and the EXISTING F/S-11.T1 ScopeFilter
-- "no fusion-code changes" (HOW-6). Once a graph record is ingested, it
is a `corpus.Record` with a `scope_ref` like any other, and
`Store.Query`'s membership check (`inChain`/`acrossEdge` in `scope.go`)
already applies before any leg runs, already treats `depends_on` and
`shares_context_with` identically (both widen through `acrossEdge`), and
already denies by default. This fixture and
`TestGraphLegNoScopeLeak`/`TestGraphLegDeclaredEdgeOpensIt` in
`graph_scope_test.go` prove that property holds for a `graph`-shaped
corpus the same way `scope_test.go`'s sibling test already proves it for
a `code`-shaped one -- no new authorization code is exercised, because
none exists to exercise.

The bounded, per-hop-gated WALK over the raw `SymbolGraph` structure
(before ingestion) -- the actual mechanism a live multi-repository graph
traversal needs -- lives in `internal/repo/graph_scope.go`
(`GatedWalk`/`CrossScopeGate`), tested against a real cross-scope cycle
in `internal/repo/graph_scope_test.go`. This fixture is the retrieval-side
half: it proves the already-ingested graph records never leak across an
un-declared scope boundary through the fusion path.

## Why scope-b is `shared`, not `scope-local`

Mirrors the sibling fixture's own reasoning exactly: a leak test whose
far-side corpus is marked private proves little, because the narrow
class alone would withhold the records regardless of any edge. Marking
`scope-b` `shared` -- the widest class core grants -- means the only
thing standing between a `scope-a` session and `scope-b`'s graph records
is the absent relationship edge, which is the control under test.

## Provenance

Hand-written scenario input, matching the sibling fixture's own
provenance note: nothing here was captured from the extractor's or the
corpus package's own output, and the tests assert a property (zero leak,
then exactly the declared records once an edge exists), never a byte-for-
byte golden.
