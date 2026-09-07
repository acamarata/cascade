# PBD Engine: PEWS Tree Store and Validator

`cascade-pbd` is the first-party builtin plugin that owns the PEWS
(Phase→Epic→Wave→Sprint→Ticket) ticket tree: loading it, validating its
structure, and (in later tickets) authoring and linting it. This page
covers the tree store and native validator, mounted as `pbd validate`.

## Tree layout

A PEWS tree is a directory rooted anywhere the caller names:

```
<root>/
  tombstones.yaml            (optional)
  epics/
    E-<LETTERS>/
      waves/
        W-<n>/
          sprints/
            S-<n>/
              tickets/
                T-<n>.yaml
```

- `<LETTERS>` is the epic's identifier: `A`..`Z`, then `AA`..`AZ`, `BA`..,
  the same bijective base-26 sequence spreadsheet columns use.
- Every `T-<n>.yaml` file decodes as a PEWS ticket-schema v2 document (see
  `plugins/pbd/internal/pews`'s `doc.go`/`schema.go`).

## Canonical ticket identity

A ticket's declared `id` field must equal the id its tree position
implies: `<phase>-E<epicNum>-W<wave>-S<sprint>-T<ticket>`, where `epicNum`
is the epic letters' bijective base-26 ordinal, zero-padded to at least two
digits (`A` → `E01`, `N` → `E14`, `AH` → `E34`), and `wave`/`sprint`/
`ticket` are the directory's own numeric strings, unpadded except for
whatever width the directory itself already carries. A mismatch is an
`identity-mismatch` violation, never a silent correction.

## Tombstone bookkeeping

A tree's optional root-level `tombstones.yaml` records retired ticket ids:

```yaml
tombstones:
  - id: P1-E14-W3-S28-T5
    reason: superseded by a later split
```

A gap in a sprint's `1..max` ticket-number sequence is only valid when the
missing slot's canonical id is recorded as a tombstone. Tombstones are
never valid `depends_on` targets, and a live ticket file must never
reclaim a tombstoned id.

## Validation

`pews.Validate` runs every structural check over a loaded `*pews.Tree` and
fails closed: whenever it finds at least one `pews.Violation`, it also
returns a non-nil `*cascade.Error` of kind `KindInvalidInput`, so a caller
that only checks the returned error still refuses. The checks:

| Violation kind | Meaning |
|---|---|
| `identity-mismatch` | a ticket's declared id does not match its tree position |
| `duplicate-id` | the same declared id appears more than once in the tree |
| `gap` | a ticket number is missing from a sprint's sequence with no matching tombstone |
| `tombstone-still-active` | a tombstoned id still has a live ticket file |
| `duplicate-tombstone` | the same id is recorded as tombstoned more than once |
| `dangling-dependency` | a `depends_on` entry names an id that exists nowhere in the tree |
| `tombstone-dependency` | a `depends_on` entry names a tombstoned id |
| `dependency-cycle` | the `depends_on` graph contains a cycle |

`pews.Report` also carries `ActiveCount`, `TombstoneCount`, and
`GateOnlyCount` (tickets with `gate_only: true`) for bookkeeping.

## `pbd validate`

The `cascade-pbd` builtin plugin (`plugins/pbd/pbd.go`) mounts `validate`
as its one command in this ticket's scope: `T3` owns authoring, `T4` owns
lint, both future commands on the same manifest.

```
pbd validate <tree-root> [phase]
```

- `<tree-root>` is required: the directory containing `epics/`.
- `phase` defaults to `P1` when omitted.
- Exit is clean (no output, exit 0) on a structurally valid tree.
- On a violation, the command returns a `*cascade.Error` of kind
  `KindInvalidInput` whose message lists every violation found, each
  tagged with its kind (`[gap] ...`, `[dependency-cycle] ...`) and, where
  applicable, the ticket file's path.

`cascade-pbd` may import `pkg/**` and its own `internal/pews` only, never
the repo's `internal/**` (Art.10.2) — this is why `pbd validate` folds its
report into the returned error's text instead of writing through
`internal/output.Writer`.

## Fixture provenance

`plugins/pbd/internal/pews/testdata/v1-goldens/` harvests the OBSERVED
BEHAVIOR of the archived structural checker
`.claude/planning/p1/.plan-audit.py` (gitignored planning-corpus tool,
named authoritative by 06-FORGE-SPEC.md §8 until this engine existed) as
JSON test vectors — never its code. See that directory's `README.md` for
full provenance (source path, MD5, harvest date).

## Files-to-database projection and the SSE update boundary

`pews.Projector` (`plugins/pbd/internal/pews/projector.go`) projects a
loaded PEWS tree into database-backed state through the Store family
contract (`pkg/provider.Store`): every ticket's canonical id, declared id,
title, weight, model class, and phase are written as one JSON record per
ticket, keyed under the `plugin.pbd` namespace (a plugin-owned namespace,
not one of `internal/storage/domains.go`'s eleven cascade.db domains — no
`DomainID` change was needed or made).

The files on disk are the source of truth. `Project`:

- **is idempotent**: running it twice over an unchanged tree performs zero
  writes and reports `Converged: true`;
- **converges**: running it again after an external edit reaches exactly
  the state a from-scratch projection over the edited tree would;
- **handles deletion as the hard case**: a ticket file removed from disk
  is diffed against the currently stored keys and its row is deleted in
  the same atomic write as any other change — never left as an orphan.

This ticket does not implement status/board, draft isolation, residue
carry-forward, lifecycle, or dispatch — those remain N/S-29.T2-T3 and
N/S-30.

### SSE update boundary

`plugins/pbd/pbd.go`'s `NewEventsHandler` builds the plugin's `GET /events`
bridge onto the repository's existing HTTP/1.1 SSE transport
(02-TARGET-STRUCTURE's boundary; not a second transport): an in-process
fan-out `bus` implements `pews.EventPublisher`, and a small `http.Handler`
forwards each projection-update notification as an `id`/`data` SSE record,
following `internal/rpc.SSEHandler`'s proven connection-loop shape without
importing `internal/events` or `internal/runtime` (`plugins/**` may import
`pkg/**` only, Art.10.2). The handler terminates cleanly — no leaked
goroutine or subscription — on context cancellation or client disconnect;
`plugins/pbd/projector_test.go`'s `TestProjectionSSERealCounterpart`
(behind the `integration` build tag, driven by `curl -N` as an independent
real client, never a self-authored SSE dialect) proves both the wire
format and the cleanup.

`NewEventsHandler` has no production caller within this ticket's scope:
mounting it on the daemon's HTTP mux and driving `Project` from a
PEWS-tree file-change trigger belongs to a later daemon composition-root
ticket (see `internal/build/testonly-allow.json`).
