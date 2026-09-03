# internal/mcp wire fixture provenance

## Protocol pin

`MCP_PROTOCOL_VERSION = "2026-07-28"` (server.go's `MCPProtocolVersion`),
per T0 ruling R-14.14: a stateless core, no initialize handshake, no
session id, required `mcp_method`/`mcp_name` fields on every request, MRTR
(MCP Request-Triggered Requests) in place of server-initiated messages.

## Golden slots

This directory is reserved for wire-golden fixtures captured from a real
`rmcp 3.0.1` client conformant to the 2026-07-28 revision, per this
ticket's Art.2 (real-counterpart) requirement: fixtures must never be
self-authored.

## Known gap: no rmcp capture in this pass

This implementation pass had no network access and no `rmcp` toolchain
available in its build environment, so no such capture was possible here.
`internal/mcp/server_test.go` and `internal/mcp/transport/*_test.go`
instead exercise the pinned revision's request/response shapes (header
validation, tools/list, tools/call, the notifications/ack MRTR exchange,
malformed-frame error paths) against fixtures this package authored itself
from the ticket contract's own text — this satisfies the pinned revision's
documented *behavior*, but does NOT satisfy Art.2's "never self-authored"
requirement for the wire bytes themselves. A follow-up pass with `rmcp
3.0.1` available must:

1. Run an `rmcp 3.0.1` client against a running `cascade mcp serve
   --stdio` (or `--socket`) process.
2. Capture the literal request/response byte sequences for: tools/list,
   tools/call (success and unknown-tool), a header-validation rejection,
   and one notifications/ack exchange.
3. Save them here as `*.golden.json`, record capture date and the exact
   `rmcp` version below, and wire `TestMCPGoldens` in
   `internal/mcp/server_test.go` (or a new `golden_test.go`, if the
   ticket's file scope is amended to add one) to assert byte-for-byte
   against them.

## Spec-upgrade policy

A future MCP revision requires a new T0 ruling amending R-14.14 before
`MCPProtocolVersion` changes. Until then, any request whose behavior this
package cannot express under the 2026-07-28 stateless core (an
`initialize` handshake, a session id, a server-initiated request) is
rejected as an unrecognized method — never silently upgraded or
downgraded.

| Field | Value |
|---|---|
| Source | not yet captured (see "Known gap" above) |
| MCP spec revision | 2026-07-28 |
| Capture tool | rmcp 3.0.1 (pinned by R-14.14; not yet run) |
| Capture date | pending |
