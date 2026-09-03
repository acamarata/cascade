# FuzzMCPFrame seed corpus

Seed files for `FuzzMCPFrame` (internal/mcp/fuzz_test.go), targeting
`mcp.ParseFrame`, the decoder for one line of untrusted MCP wire input on
both transports (stdio directly; the socket transport indirectly, since
`mcp.dispatch`'s JSON-RPC params carry the same frame shape).

- Source: hand-authored, not captured from a live client — these seeds
  exercise ParseFrame's own decode boundary (valid/invalid JSON, wrong
  jsonrpc version, missing method, wrong field types), not protocol
  semantics. Protocol-shape fixtures live in `internal/mcp/testdata/`.
- Format: one JSON object per `*.json` file, loaded verbatim as a fuzz seed
  string.
- Capture date: 2026-09-03.
