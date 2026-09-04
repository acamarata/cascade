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

## Gate decision (W1 hardening gate): explicit deferral, risk stated

The W1 gate had to either capture these goldens from a real client or
record an explicit deferral with the risk stated. It records the deferral,
for a reason that is not "the toolchain was unavailable".

`rmcp 3.0.1` is downloadable and a Rust toolchain is present, so the
original blocker is gone. What remains is a protocol mismatch. R-14.14
pins this server to a stateless core with no `initialize` handshake and a
required `mcp_method`/`mcp_name` pair on every request; `server.go`
rejects any frame missing them (`missing required mcp_method/mcp_name
fields`). A stock rmcp client opens with `initialize` and sends neither
field, so driving this server with one yields rejection frames, not
conformant `tools/list` or `tools/call` bytes. There is no off-the-shelf
client that speaks this dialect, so "captured from a real conformant
client" cannot be satisfied by any client that exists today.

THE RISK, stated plainly: these fixtures prove only that the
implementation agrees with itself. If the frame layout in the contract
text is wrong, or the server drifts from it, nothing here will notice. A
real interoperability defect would ship undetected. That is the failure
shape a golden fixture exists to prevent, and it is not prevented here.

Closing it needs one of two things, and both are decisions above this
gate: publish the dialect so a conformant client can be written (then
capture against it), or move the server onto the standard MCP handshake so
an existing client is a real counterpart. Recorded for the wave-2 and
release gates rather than silently carried.

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
| Source | not captured; explicit deferral recorded at the W1 hardening gate |
| MCP spec revision | 2026-07-28 |
| Capture tool | rmcp 3.0.1 (pinned by R-14.14; not yet run) |
| Capture date | pending |
