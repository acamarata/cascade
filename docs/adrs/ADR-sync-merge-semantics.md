# ADR: Sync merge semantics

- Status: Accepted (spike finding — one property REFUTED, see Decision)
- Ticket: P1-E13-W3-S27-T5 (12-QUALITY-CONSTITUTION.md Art.12 risk spike, no
  dependency on and no scope claim over the ticket it de-risks)
- Consumed by: Q/S-38.T2 (production sync engine) at that ticket's own
  scheduling. Q/S-38.T2 depends on this ticket and must verify the fixture
  corpus digest below before its own merge tests run; it deletes every
  `//go:build spike` file in `internal/syncmerge` when its engine lands.
- Evidence: `internal/syncmerge/testdata/fixtures/` (16 fixture files, 4
  domains); `internal/syncmerge/testdata/README.md` (provenance); every
  merge function and property test in `internal/syncmerge/*.go` /
  `*_test.go` under the `spike` build tag.

## The question

Before Q/S-38.T2 designs the production sync engine, this spike answers: do
Cascade's four sync-domain merge strategies (config LWW, memory/conversation
append-merge with tombstones, phase-state git-carried, blob content-address
union) converge without silent data loss under adversarial inputs?

## Answer, stated plainly

**Three of the four domains converge cleanly under every adversarial case
tested. The fourth (memory/conversation) converges on four of its five
required properties but its associativity property is REFUTED under the
practical pairwise-merge-chaining implementation shape, with a committed
counterexample.** This is a real finding, not a self-confirming test: the
counterexample was found by a deterministic property-based generator run
(seeded `math/rand`, no self-authored expected values for this domain) and
is preserved as `TestMemoryAssociativityRefuted` in
`internal/syncmerge/properties_test.go`, hardcoded so the finding survives
independent of any future re-run's random seed.

## Per-domain verdict

| Domain | Verdict | Properties checked |
|---|---|---|
| Config (LWW) | **CONVERGES** | commutativity, idempotence, associativity, no-loss — all hold, fixtures + 50 generated trials, zero violations |
| Memory/conversation (append-merge + tombstones) | **CONVERGES for commutativity, idempotence, tombstone dominance, no-loss (0/200 violations); REFUTED for associativity (9/200 generated trials violate it)** | see "The associativity refutation" below |
| Phase-state (git fetch+fast-forward) | **CONVERGES** (by refusal) | clean fast-forward accepted; non-fast-forward and disjoint-branch divergence both refused with `ErrPhaseStateDiverged`, never engine-merged — verified against the real `git` binary, not a simulation |
| Blobs (content-addressed union) | **CONVERGES** | commutativity, idempotence, associativity, no-loss — all hold trivially by construction (union keyed by content digest); the re-chunked-payload case intentionally produces two surviving hashes (documented constraint, not a defect) |

## Ordering and tie-break rules (config domain, R-21.223)

Ordering is the server-assigned monotonic **revision**, then the persisted
**hybrid logical clock (HLC)**, then the writer **node id** — in that exact
order. Wall-clock time is never compared. Verified fixtures:
`config-revision-tie-break.json` (equal revision resolved by HLC then node
id, exactly once), `config-delete-vs-write.json` (deletion is an ordinary
value under this ordering — config has no tombstone-dominance rule, unlike
memory), `config-clock-skew-24h.json` (a local write with wall clock 24h
ahead of the server's still loses because its revision is behind — proves
wall-clock is genuinely never consulted), `config-three-way-partition.json`
(three independently-partitioned replica views of one record converge to
the same value regardless of pairwise merge order).

Every losing local write is journaled with both revisions, both HLC
values, both node ids, and both content hashes
(`ConfigJournalEntry`, verified by `TestConfigJournalCarriesBothSides`).

## Memory/conversation domain rules (R-21.223)

Records carry a globally unique immutable id and a version vector.
Concurrent updates resolve by version-vector dominance, falling back to the
same revision/HLC/node-id ordering when neither vector dominates. A
tombstone for a record id dominates every concurrent update to it, in
either merge order (`TestMemoryTombstoneNeverPruned`,
`memory-resurrection-suppressed.json`: a concurrent update with revision
500 and HLC 999 still cannot resurrect a tombstone recorded at revision 5).
Tombstones are never pruned in P1 (`[sync].tombstone_retention = never`); a
peer cursor predating the oldest retained tombstone is refused with typed
`ErrCursorTooOld` (wrapping the frozen `KindConflict`) rather than
resynced incrementally (`memory-stale-peer-cursor-too-old.json`).

### The associativity refutation

`resolveMemory` snapshots the WINNING side's scalar tie-break fields
(revision, HLC, node id) when neither side's version vector dominates the
other, while separately computing the union of both sides' version
vectors as the carried-forward vector. This decouples the retained scalar
snapshot from the vector that will be compared next. Two different
association orders accumulate the SAME final version vector via different
intermediate scalar snapshots; when a later merge step compares against
the fully-accumulated vector, one order can find genuine vector dominance
that the other order's earlier, partial vector state never surfaced —
so the two orders can pick two different winning scalar snapshots while
still agreeing on the vector. The committed counterexample
(`TestMemoryAssociativityRefuted`) has both `merge(merge(A,B),C)` and
`merge(A,merge(B,C))` converge to version vector `{node-a:8, node-b:9}` but
disagree on which HLC (16 vs 7) is attached to it. 200 deterministic
generated trials found this in 9 (4.5%) — not a rare edge case.

Commutativity, idempotence, tombstone dominance and no-loss all held with
**zero** violations across the same 200 trials — the refutation is
specific to three-or-more-way chained association, not a general defect
in the pairwise merge.

**Recommendation for Q/S-38.T2:** do not implement multi-peer memory sync
as repeated pairwise `merge(accumulator, peer)` calls chained across peers
in arbitrary order, and expect the result to be order-independent. Either
(a) always merge every peer's state directly against one canonical
accumulator in a single unordered reduction over the full set (never
through a second peer's already-merged copy — mathematically this still
doesn't fix the decoupling unless the accumulator design changes; needs a
join-semilattice value type where the scalar tie-break travels WITH the
vector, not beside it), or (b) redesign the per-id value as a proper CRDT
register that retains the full set of concurrent (scalar, vector) pairs
until a total order is applied once, over the complete set, rather than
folding pairwise. Either amendment must be made before Q/S-38.T2's design
is finalized; this spike does not choose between them, only demonstrates
that the literal R-21.223 rule as stated is insufficient to guarantee
associative N-way convergence.

## Phase-state domain rules (R-21.223, Art.2 external contract)

Phase state is git-tracked YAML; carriage is FETCH + FAST-FORWARD ONLY. A
non-fast-forward or conflicting merge is refused with typed
`ErrPhaseStateDiverged` (wrapping `KindConflict`), journaled with both
refs, never engine-merged. `TestPhaseMergeGit` builds real two-branch git
repositories fresh in `t.TempDir()` on every run and drives the real `git`
binary (git 2.51.0, captured 2026-09-07 — see `testdata/README.md`):
clean fast-forward succeeds and produces the expected merged content;
independently-diverged branches from a shared ancestor are refused;
wholly disjoint (unrelated) histories are also refused, never
auto-merged as an octopus/unrelated-histories merge.

## Blob domain rules (R-21.223)

Blobs land in staging and are admitted only when the recomputed BLAKE3
digest (real `github.com/zeebo/blake3`, already a direct dependency)
equals the declared content address; a mismatch — including a truncated
transfer, which changes the digest — discards the staged bytes and returns
typed `ErrBlobDigestMismatch` (wrapping `KindIntegrity`)
(`blob-staging-digest-mismatch.json`). Content-addressed union then
operates on admitted blobs only, keyed by address, which makes it
trivially commutative/associative/idempotent (`blob-union-identical.json`,
`blob-union-disjoint.json`). **Constraint for Q/S-38.T2's dedup pass:** the
same logical payload chunked two different ways produces two distinct
content addresses, and CA union does not deduplicate across chunkings —
both survive (`blob-union-rechunked.json`). A future dedup/rechunk-aware
layer is Q/S-38.T2's responsibility, not this union step's.

## Sensitivity filtering (R-21.223)

Every record is filtered by its immutable sensitivity tier BEFORE
serialization: `local-only` and `restricted` records never serialize, in
any domain (`FilterSensitive`, `TestSensitivityFilteredBeforeSerialization`).
Each exclusion is journaled by record id plus the policy version, and the
transport cursor advances past excluded ids explicitly so a later policy
or classification change can rescan them.

## Ratified domain mapping (R-21.223)

This spike's fixtures use only ratified SQLite domain names: conversation
records map to `context`; registry and account metadata maps to `config`.
No `registry`, `accounts` or `conversation` domain is named or created.

## Fixture corpus digest

The fixture corpus is `internal/syncmerge/testdata/fixtures/` (16 JSON
files). Its BLAKE3 digest, computed as the hex digest of the sorted
filename-then-contents concatenation (`sha256sum`-style: for each file in
`sort`-ed filename order, hash `filename + "\n" + contents`, then BLAKE3
the concatenation of those per-file digests):

```
BLAKE3(fixture-corpus) = 291a77c5f48eba2b3d40f71b7255a227478baae0fc3224a00a3b71bf884b1cf7
```

Q/S-38.T2 must recompute this digest over the same corpus before trusting
these fixtures as its acceptance suite, and must fail its own build if the
digest is absent or the fixtures were altered without an ADR revision.

## Not in scope

The production sync engine (Q/S-38.T2), any RPC or CLI surface, cross-node
dispatch routing, conflict-resolution UI (V/S-47), or the sync domain
scheduler. This ticket adds no dependency edge to Epic Q.
