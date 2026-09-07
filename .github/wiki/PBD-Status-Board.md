# PBD Status/Board (N/S-29.T4)

Two read-only surfaces over the PEWS ticket tree for one phase: a summary
count and a grouped board. Neither writes to the tree, and neither
invents a status field or board layout beyond the projected row's own
`weight`/`model_class` fields.

## CLI

```
cascade pbd summary <tree-root> [phase]
cascade pbd board   <tree-root> [phase]
```

`phase` defaults to `P1` when omitted, matching `pbd validate`/`pbd lint`'s
own convention.

**Naming note:** the contract names this verb `status`. `pkg/plugin`'s
manifest validator (rule R5, the `07-CLI-COMMAND-TREE.md` reserved-word
blocklist) rejects any plugin `provides.commands` entry literally named
`status` — it is one of the 9 reserved top-level utility verbs, checked by
bare name regardless of which plugin namespace it would mount under. The
CLI verb is therefore `summary`. The JSON-RPC method and MCP tool names
below keep the contract's literal `status` spelling, since R5 only
constrains `CommandSpec.Name`.

## JSON-RPC

| Method | Request | Result |
|---|---|---|
| `plugin.pbd.status` | `{"root": "...", "phase": "P1"}` | `StatusReport` |
| `plugin.pbd.board` | `{"root": "...", "phase": "P1"}` | `BoardReport` |

`root` is required; `phase` defaults to `P1`.

```json
// StatusReport
{
  "phase": "P1",
  "total": 2,
  "by_weight": {"S": 1, "M": 1},
  "by_model_class": {"build": 1, "heavy": 1}
}

// BoardReport
{
  "phase": "P1",
  "columns": [
    {"model_class": "build", "tickets": [{"canonical_id": "...", "title": "...", "weight": "S"}]},
    {"model_class": "heavy", "tickets": [...]}
  ]
}
```

Board columns are grouped by `model_class` (the closest existing field to
a workflow stage — no new status/state field), sorted alphabetically for
determinism, not the mech/build/heavy/review/arbiter execution order
(kept unexported in `internal/pews`).

## MCP

Mirrored per `07-CLI-COMMAND-TREE.md`'s rule (`cascade_` + the RPC method
with dots turned to underscores): `cascade_plugin_pbd_status` and
`cascade_plugin_pbd_board`. Both are declared with the plugin's `read`
grant only, so `internal/mcp`'s policy filter exposes them; no write verb
exists.

## Read path

`pbd summary`/`pbd board` (and `DispatchTool`) read the PEWS ticket tree
directly (`internal/pews.Store.Load`) and build the same row shape T1's
projector (`internal/pews.Row`) already defines — the ONE read model,
regardless of source. `ReadRowsFromStore` is the literal
T1-files-to-database-projection read path (scans `plugin.pbd`, decodes
each row), for a caller that already holds a `pkg/provider.Store`; neither
`RunCommand` nor `DispatchTool`'s fixed signatures carry a channel to
inject one yet, so it has no production caller in this ticket (see
`internal/build/testonly-allow.json`).

## Local checks

```
go test ./plugins/pbd -run '^TestStatusBoardCommands$'
go test ./plugins/pbd -run '^TestStatusBoardMirroredMethods$'
go test ./plugins/pbd -run '^TestStatusBoardRealCounterparts$'
go test ./plugins/pbd/... -run '^TestStatusBoardPlatformParity$'
go test -race ./plugins/pbd/...
go vet ./plugins/pbd/...
```
