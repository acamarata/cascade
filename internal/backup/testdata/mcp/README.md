# MCP Backup List Fixture Provenance

**Art.2 evidence (P1-E19-W4-S42-T3):** This directory holds the fixture
record for the `cascade_backup_list` MCP tool registered by `MCPRegistration`
in `internal/backup/mcp.go`.

## Tool identity

| Field     | Value                                        |
|-----------|----------------------------------------------|
| Name      | `cascade_backup_list`                        |
| Plugin ID | `cascade.core.backup`                        |
| Grants    | `["read"]`                                   |
| Mutations | none — read-only, no elevated verbs          |

## Session trace

Captured by piping real, hand-written MCP JSON-RPC request lines directly
into the real `cascade mcp serve --stdio` binary (built from this commit,
`go1.26.6`, commit `79bc5cc`, captured 2026-09-12) over an empty
`CASCADE_HOME` (a fresh `mktemp -d`, no targets configured). This is the
real production stdio transport (`internal/mcp/transport/stdio.go`) and the
real composition root (`cmd/cascade/mcp.go`'s `buildRPCServer`/`mcp.go`
wiring `backup.MCPRegistration`), not a mock or an in-process handler call.
The client here is a hand-built line-framed JSON-RPC request (the exact
shape `internal/mcp/server.go`'s stateless `Frame` type requires:
`mcp_method` matching `method`, and a non-empty `mcp_name`) rather than the
MCP Inspector GUI or the TypeScript SDK, since this sandbox runs headless;
the wire bytes below are the real, unedited process output.

The result carries zero snapshots because the ephemeral `CASCADE_HOME` used
for this capture has no configured backup target — this is the true "no
targets configured" response, not a truncated positive example, and it is
what a fresh install actually returns. `TestListSnapshotsRealTarget` and
`TestListSnapshotsWithRecordedOutcome` (`internal/backup/mcp_list_test.go`)
separately prove the same code path with a real signed snapshot and a real
recorded outcome present, decrypting a real manifest — that assertion does
not depend on this fixture.

### Request 1: `tools/list`

```json
{"jsonrpc":"2.0","id":1,"method":"tools/list","mcp_method":"tools/list","mcp_name":"mcp-fixture-capture","params":{}}
```

### Response 1

```json
{"jsonrpc":"2.0","id":1,"result":{"protocol_version":"2026-07-28","tools":[{"name":"cascade_backup_list","description":"List backup snapshots and manifest metadata","plugin_id":"cascade.core.backup"}]}}
```

### Request 2: `tools/call` (`cascade_backup_list`)

```json
{"jsonrpc":"2.0","id":2,"method":"tools/call","mcp_method":"tools/call","mcp_name":"mcp-fixture-capture","params":{"name":"cascade_backup_list","arguments":{}}}
```

### Response 2

```json
{"jsonrpc":"2.0","id":2,"result":{"output":{"snapshots":[]}}}
```

### Process stderr (informational, not part of the MCP wire)

```
warning: daemon not running; running in embedded (daemonless) mode
```

## Security attestation

- `cascade_backup_list` is **read-only**: it calls `ListSnapshots` which
  reads manifest metadata only. No snapshot bytes, no secret values, and no
  age-encrypted object content is included in any MCP response.
- The tool is registered with grant `"read"` only. The MCP registry's closed
  grant filter refuses any call without that grant in the caller's manifest.
- Elevated verbs (`backup.export`, `backup.import`, `backup.create`,
  `backup.restore`) are **not registered** in the MCP tool registry.
  See 07-CLI-COMMAND-TREE.md's MCP-exclusion rationale for elevated verbs.

## How to regenerate

```bash
go build -o /tmp/cascade ./cmd/cascade
export CASCADE_HOME="$(mktemp -d)"
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"tools/list","mcp_method":"tools/list","mcp_name":"regen"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","mcp_method":"tools/call","mcp_name":"regen","params":{"name":"cascade_backup_list","arguments":{}}}' \
  | /tmp/cascade mcp serve --stdio
```
