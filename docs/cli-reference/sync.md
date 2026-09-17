# `cascade sync`

Reference for the `sync` noun (07-CLI-COMMAND-TREE.md §sync), implemented
by P1-E17-W4-S38-T3 over the S-38.T1 engine and the S-38.T2 conflict
journal. Every verb is a thin mirror of a `sync.*` RPC method, so the CLI
and the daemon API answer the same questions with the same code.

| Verb | RPC method | MCP | Elevated |
|---|---|---|---|
| `sync status` | `sync.status` | ✦ exposed | no |
| `sync run [--domain <d>]` | `sync.run` | not exposed | no |
| `sync conflicts list` | `sync.conflicts_list` | ✦ exposed | no |
| `sync conflicts resolve <id> --keep <side>` | `sync.conflicts_resolve` | **never** | when `--keep local` |

`--json` works on every verb and emits the standard versioned envelope.

## `cascade sync status`

Reports, for every registered domain: its sync class, the merge strategy
it would use, whether this peer's trust tier may sync it (and why not),
the cursor position it has reached, and the number of journaled conflicts
still open.

```
$ cascade sync status
peer tier: controller
open conflicts: 0

DOMAIN                STRATEGY                      SYNCS  AT  WHY NOT
blobs/blobs           content-address-union         yes    12
config/accounts       metadata-only-server-primary  yes    41
config/config         server-primary-lww            yes    41
memory/memory         append-merge-tombstones       yes    7
```

**Every registered domain is listed, including the ones this peer may not
sync.** A report that omitted them would answer "why is memory not
syncing?" by omitting memory. An ineligible domain shows `no` in the SYNCS
column and the reason in WHY NOT.

**`?` in the AT column means the position could not be read**, not
position zero. Zero is a real position — a domain that has never synced is
at zero — so the two are separate facts in the JSON output as well
(`position` and `position_known`). A machine with no store open reports
`?` for every domain.

## `cascade sync run [--domain <d>]`

Syncs one domain, or every domain this peer's tier permits.

**One domain's failure does not fail the run.** A sync that abandoned four
healthy domains because the fifth's remote was down would make a whole
fleet wait on one machine, so each domain's outcome is reported
separately:

```
$ cascade sync run
DOMAIN           RESULT
config/config    synced
memory/memory    failed: dial tcp 10.0.0.4:22: connect: connection refused
```

A `--domain` nobody registered is refused, naming what is registered — a
caller who typed it asked for something, and syncing everything instead
would be a bug they would not find for weeks. The request is validated
before the machine's own capability is reported, so the typo surfaces even
on a machine with no sync session open.

## `cascade sync conflicts list`

Renders the journal every merge strategy writes to: the record, its domain
and subkind, the strategy that decided, the resolution, and both sides.
The discarded side is shown because that is what an operator is looking
for — the thing that went.

```
$ cascade sync conflicts list
RECORD  DOMAIN         STRATEGY            RESOLUTION   KEPT           DISCARDED
cfg-3   config/config  server-primary-lww  server-won   server@9 hs    laptop@8 hl
```

A side carried by a git ref shows the ref; a side that does not exist (a
refused merge has no winner) shows `-`, so a missing value is
distinguishable from a mis-aligned column.

## `cascade sync conflicts resolve <record-id> --keep <side>`

Settles one journaled conflict.

- `--keep server` accepts what the merge already decided. It changes
  nothing and is **not** elevated.
- `--keep local` **discards the server's copy**, overriding the authority
  a server-primary domain is defined by. It is an elevated verb
  (06-FORGE-SPEC §5.14).

**The side is a flag, never a prompt.** A non-interactive run has to be
able to make the choice; what a prompt could not settle is whether this
machine may act on it. That is what the three refusals below are for:

| Situation | Behavior |
|---|---|
| `--keep` missing or not `server`/`local` | Refused as invalid input; never defaulted |
| Record not in the journal | Refused, pointing at `sync conflicts list` |
| `--keep local` on Windows | Refused: tier-2 platform (06 §5.14) |
| `--keep local` with `CASCADE_NO_INPUT=1` and no `--yes` | Hard error, never a hang (08 §2) |
| `--keep local` with no elevation gate wired | Refused: a machine that cannot check must not be the one that allows it |

Over RPC, `sync.conflicts_resolve` is gated by the shared elevation
middleware, which demands and verifies an attestation before the handler
runs. **It is never an MCP tool**: a model that could discard the server's
copy of a config record on its own reasoning is a surface nobody asked
for. `internal/mcp/coretools` asserts that no registered tool is bound to
an elevated verb.

## Exit codes

Per the error taxonomy's wire mappings (A/S-01.T7): `invalid-input` for a
bad flag or an unknown side, `not-found` for an unknown domain or an
unjournaled record, `elevation-required` for a refused elevated
resolution, `unsupported` for the Windows tier-2 refusal, and
`unavailable` when no run path or no engine is composed.

## Implementation status

`sync run` currently reports that no run path is wired: opening a sync
session between two machines is P1-E17-W4-S38-T5's, which needs a second
machine. The verb refuses rather than reporting a sync that never
happened. `status`, `conflicts list` and `conflicts resolve` run against
the real engine and the real journal.
