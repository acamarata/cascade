# PBD Authoring

`pbd create`, `pbd edit`, and `pbd move` are the `cascade-pbd` builtin
plugin's authoring commands (`plugins/pbd/pbd.go`, mounted alongside T2's
`pbd validate` and T4's `pbd lint`). They are the only commands in this
plugin that write to the PEWS tree; every other mounted command is
read-only.

## Usage

```
pbd create <tree-root> <ticket-file> [phase]
pbd edit   <tree-root> <ticket-id> <ticket-file> [phase]
pbd move   <tree-root> <from-id> <to-id> [phase]
```

- `<tree-root>` is required: the directory containing `epics/`.
- `<ticket-file>` is a path to a ticket-schema v2 YAML document (the same
  shape a `T-*.yaml` file in the tree carries).
- `phase` defaults to `P1` when omitted.
- `create` authors `<ticket-file>`'s content at the canonical tree
  position its own `id` field implies.
- `edit` overwrites the ticket already filed at `<ticket-id>`'s canonical
  position with `<ticket-file>`'s content; the file's own `id` must equal
  `<ticket-id>`, or edit refuses before touching the tree.
- `move` relocates the ticket at `<from-id>` to `<to-id>`'s canonical
  position, rewriting its declared `id` field to match.
- Exit is clean (no output, exit 0) on success.

Local check: `go test ./plugins/pbd -run '^TestAuthorCommands$'` (or
`./plugins/pbd/internal/pews -run '^TestAuthor'` for the engine alone).

## The validation boundary

Every write goes through the same `pews.Lint` (which composes
`pews.Validate`) that `pbd validate`/`pbd lint` run — entirely **in
memory**, before a byte touches disk. Authoring builds the tree the write
would produce, lints it, and only proceeds to disk if that candidate tree
is clean. A write that would leave the tree structurally unsound
(identity mismatch, a duplicate or gapped ticket number, a dangling or
tombstoned dependency, a cycle) or contract-incomplete (a missing field,
a cardinality violation, a CR/weight mismatch, a missing Article-3 DoD
phrase, and so on) is refused, and nothing is written.

This means an authored or edited ticket can never be an invalid one: the
tree a running `pbd validate`/`pbd lint` would see is always the tree
authoring already checked before writing.

## No-clobber and refusal shape

- `create` refuses (`KindConflict`) when a ticket file already exists at
  the target position; use `edit` instead.
- `edit` refuses (`KindNotFound`) when no ticket exists at the target
  position yet; use `create` instead.
- `move` refuses (`KindNotFound`) when no ticket exists at `<from-id>`,
  refuses (`KindConflict`) when a ticket already exists at `<to-id>`, and
  refuses (`KindInvalidInput`) when `<from-id>` equals `<to-id>`.
- Every id (a ticket's own declared `id`, or `<from-id>`/`<to-id>`) must
  parse as a canonical, correctly zero-padded ticket id under the target
  phase, or the command refuses (`KindInvalidInput`) before loading the
  tree.
- Every refusal above writes and removes nothing: the tree on disk is
  bit-for-bit unchanged from before the call.

## Atomicity

The one file each successful call writes is written atomically: the
ticket is encoded to a temporary file in the same directory, synced,
closed, and then renamed over the target. A failure at any step before
the rename removes the temporary file and leaves the target untouched;
the rename itself is atomic, so no reader of the tree — including a
concurrent `pbd validate`/`pbd lint` — ever observes a partially written
ticket file.

## Round-trip fidelity

Authoring never rewrites a ticket's contract. The bytes written to disk
are exactly the ticket-schema v2 codec's own `EncodeTicket` output —
fields in `06-FORGE-SPEC.md` §1's normative order, with every unset
optional flag omitted rather than written as an explicit zero value.
Decoding a written file and re-encoding it reproduces the same bytes.

## Scope

`pbd create`, `pbd edit`, and `pbd move` mount only those three commands
through the `cascade-pbd` builtin-plugin namespace, alongside the
already-landed `validate` and `lint`. This ticket adds no core CLI noun,
UI, status/board surface, ticket lifecycle (claim/step/CR/QA/done), or
dispatch surface — those belong to later PBD tickets.
