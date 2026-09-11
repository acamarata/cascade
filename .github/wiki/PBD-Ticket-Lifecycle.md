# PBD Ticket Lifecycle (N/S-30.T1)

The claim/step/CR/QA/done state machine over a PEWS ticket, and the
entity-journal that records every transition. No lifecycle field exists
on the ticket contract itself: state is always derived by replaying the
journal, never stored as free text (see Contradictions below).

## States and events

Six states, in transition order: `unclaimed` (implicit start, no journal
entries) -> `claimed` -> `step` -> `cr` -> `qa` -> `done` (terminal).

Five events a caller may request: `claim`, `step`, `cr`, `qa`, `done`.
The transition table is closed and exhaustive (asserted against the spec
in `internal/pews/lifecycle_test.go`, never against a second copy of
itself): `step` may repeat from `step` (multiple tasks), `cr` may repeat
from `cr` (multiple review passes), `qa` may repeat from `qa` (multiple QA
passes). Any other pair — a step before a claim, a second claim, a done
before a qa pass — refuses with a typed `KindConflict` error, never
coerced to a nearby legal state. An unknown or unparseable event refuses
with `KindInvalidInput`.

## CR/QA level carriage

A `cr` transition's level must be one of the ticket's own declared
`cr_level` tokens (e.g. a ticket declaring `CR-A+CR-B+CR-C` accepts a
`cr` pass naming `CR-A`, `CR-B`, or `CR-C`, one review at a time). A `qa`
transition's level must equal the ticket's declared `qa_level` exactly
(a single token, never a combination). Neither invents a level the
ticket contract did not already declare.

## CLI

```
cascade pbd claim <tree-root> <ticket-id> <operation-id> [phase]
cascade pbd step  <tree-root> <ticket-id> <operation-id> [phase] [event] [cr_level] [qa_level] [note]
cascade pbd done  <tree-root> <ticket-id> <operation-id> [phase]
```

`step`'s optional `event` is `""`/`"step"` (default), `"cr"`, or `"qa"` —
07's ratified `pbd` namespace mounts exactly `claim`/`step`/`done`, so CR
and QA transitions ride `step`'s own event field rather than a fourth or
fifth verb.

## JSON-RPC

| Method | Request | Result |
|---|---|---|
| `plugin.pbd.claim` | `{"root","ticket","operation_id","phase"}` | `pews.JournalEntry` |
| `plugin.pbd.step` | `{"root","ticket","operation_id","phase","event","cr_level","qa_level","note"}` | `pews.JournalEntry` |
| `plugin.pbd.done` | `{"root","ticket","operation_id","phase"}` | `pews.JournalEntry` |

`root`, `ticket`, and `operation_id` are required. No MCP tool is
declared for any of the three: lifecycle writes are not MCP-safe.

## Entity journal

`internal/pews.JournalStore` (`Append`/`Replay`) is this ticket's
plugin-local entity-journal capability. `FileJournalStore`
(`plugins/pbd/lifecycle.go`) is its shipped implementation: an
append-only YAML sidecar (`lifecycle-journal.yaml`, sibling to
`tombstones.yaml`), one entry per transition. `CurrentState` replays an
entity's entries through the transition table to derive its current
state — the only way state is ever known.

## Local checks

```
go test ./plugins/pbd/internal/pews -run '^TestTicketLifecycle'
go test ./plugins/pbd -run '^TestTicketLifecycleCommands$'
go test ./plugins/pbd -run '^TestTicketLifecycleRealJSONRPCCounterpart$'
go test ./plugins/pbd/... -run '^TestTicketLifecyclePlatformParity$'
go test -race ./plugins/pbd/...
go vet ./plugins/pbd/...
```

## Contradictions (both sides quoted)

**No status field.** R-21.276 and this ticket's own plan text describe a
ticket lifecycle in terms of ticket "status", but `schema.go`'s Ticket
contract (P1-E14-W3-S28-T1) is closed at 17 normative fields plus 5 extra
flags, and `DecodeTicket` "never panics... [and refuses] an unknown or
missing field." `status.go` (N/S-29.T4) independently states it adds "no
invented status field... beyond `pews.Row`'s own fields." Both are
outside this ticket's `files_scope`. Resolution: lifecycle state is never
written onto the ticket contract at all — it is computed purely by
replaying the entity journal this ticket defines, generalizing
`residue.go`'s own binary (tombstoned/not) terminal-state signal into a
full six-state machine.

**Entity journal cannot be imported.** The ticket's HOW section directs
"record lifecycle activity through the entity-journal capability supplied
by Epic M" (`internal/fleet/journal`, P1-E13-W3-S27-T1). But
`internal/fleet/journal.Store.Append` takes a `journal.Kind` — a named
type over that package's own eight-kind enum — and `plugins/pbd` may
import `pkg/**` only, never `internal/**` (Art.10.2, enforced by both
`internal/build/arch_test.go` and `.golangci.yml`'s `depguard` rule).
Even a locally duck-typed interface cannot be satisfied by
`*journal.SQLiteStore` without that import, since Go interface
satisfaction requires identical named parameter types, not merely
identical underlying types. Resolution: `internal/pews.JournalStore` is a
plugin-local instantiation of the same append/replay capability, declared
with only basic Go types; `FileJournalStore` is its shipped
implementation. A future ticket wiring `cascade-pbd` into the daemon may
adapt a real `journal.SQLiteStore` to this same interface at the
composition root, which is where such an adapter belongs (see
`internal/build/testonly-allow.json`'s `plugins/pbd.ClaimRPC`/
`StepRPC`/`DoneRPC` entries).
