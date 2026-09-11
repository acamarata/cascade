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

## Draft-phase isolation (N/S-29.T2)

A phase is a draft when its root carries a `phase.yaml` document with
`draft: true` — the only representation (R-21.276): no directory
convention, no marker file, no separate state value. A missing
`phase.yaml` means the phase is not a draft, matching `Phase{}`'s own zero
value. `pbd`'s tree-store, validator, and authoring engine all key off
this single field.

### The isolation boundary

`Store.Load` (unchanged signature) always excludes a draft phase's
tickets: it reads `phase.yaml` first, and when the phase is a draft it
returns `&Tree{Draft: true}` with `Tickets`/`Tombstones` both nil —
**without opening a single ticket file**. A malformed or incomplete draft
ticket therefore can never break a default (active) read: the file is
never decoded at all for an excluded phase.

`Store.LoadWithOptions(LoadOptions{IncludeDrafts: true})` is the only way
a draft phase's tickets enter a returned `Tree`. When drafts are included,
ticket decoding uses `DecodeDraftTicket` in place of `DecodeTicket`: it
runs the same fail-closed pipeline (malformed YAML, unknown fields, wrong
shape, `cr_level`/`qa_level` validity) but skips the `weight`/
`model_class` enum check, per R-21.276's "a draft phase accepts ticket
edits without weight/model validation." Authoring (`Create`/`Edit`/`Move`)
always loads with `IncludeDrafts: true` — a draft is edited normally,
against its own sibling tickets — and additionally drops the
`weight`-to-`cr_level` floor lint issue (`ruleCRWeight`) for a draft
candidate tree before deciding pass/fail.

`Validate`'s `Report` carries a `Draft` field mirroring the tree's own
flag, so a clean, draft-excluded report (`ActiveCount: 0, Draft: true`)
is never mistaken for a clean active one.

A draft phase cannot be built: `RequireBuildable(tree)` returns a typed
`cascade.KindConflict` refusal for a draft tree. No build/dispatch entry
point calls it yet — that belongs to N/S-30 (see
`internal/build/testonly-allow.json`).

### What this ticket does not cover

Residue carry-forward (`CarryForward(src, dst PhaseID)`), status/board,
lifecycle, projection, and dispatch remain N/S-29.T1/T3-T4 and N/S-30's.

## Residue carry-forward (R-21.276)

`CarryForward(src, dst PhaseID) error` moves a closing phase's unfinished
work into a draft successor. `PhaseID{Root, Phase string}` names one
phase's tree — the same `(root, phase)` pair `Store` already takes.

**Residue** is every `src` ticket not already recorded in `src`'s own
`tombstones.yaml`. (R-21.276 defines residue by ticket "status", but no
status field exists anywhere in this engine yet — `Ticket`'s 17+5-field
contract has none, and `pews.Row` adds none by design. Tombstone
presence is the one terminal-state signal the tree already persists; a
real completion field remains a gap for a later ticket to add.)

**The carry.** Each residue ticket is assigned a new canonical id in
`dst`'s own id space — the same epic/wave/sprint coordinates as in
`src`, at the smallest free ticket number at or above its original one
(a `dst` collision bumps the number, never clobbers). Every
`depends_on` entry naming another carried ticket is rewritten to that
ticket's new id; an entry naming a ticket that was not carried (already
done, or tombstoned before this call) is preserved verbatim. The copy is
written through the same underlying atomic-write primitive `Create`/
`Edit`/`Move` use, not through `Create`'s Lint preflight: carry-forward
preserves an existing contract exactly as it already reads in `src`,
and re-gating it against contract-lint policy is a separate concern.

**The stamp.** R-21.276 requires marking each copy with
`carried_from: <src-ticket-id>`, but `Ticket`'s field set is closed and
fails closed on an unknown key. The stamp instead lives in a root-level
`carried-forward.yaml` sidecar in `dst` (`new_id` → `carried_from`),
mirroring `tombstones.yaml`'s own existing root-level convention rather
than reopening the frozen ticket contract.

**The originals.** Once every copy is written, `CarryForward` appends
one `tombstones.yaml` entry per carried ticket to `src` and removes its
original file — a live ticket file at an id its own `tombstones.yaml`
also names is itself a structural violation (`Validate`'s
`ViolationTombstoneLive`), so tombstoning always means both parts.

**The refusal.** `CarryForward` checks `dst.Draft` (via
`LoadWithOptions{IncludeDrafts: true}`) before any write and returns a
typed `cascade.KindConflict` refusal when `dst` is not a draft phase —
leaving both phases byte-for-byte untouched. An empty residue set is a
clean no-op.

No production caller exists yet: this ticket's own scope explicitly
excludes status/board, projection, lifecycle, and dispatch. See
`internal/build/testonly-allow.json`'s entry for
`plugins/pbd/internal/pews.CarryForward` — N/S-30's phase-close seam is
the expected caller.
