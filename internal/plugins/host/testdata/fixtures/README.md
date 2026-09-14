# Plugin ABI host-call fixtures

`plugin_abi_http_call.json` and `plugin_abi_secret_ref.json` are literal
JSON-RPC 2.0 notification frames in the wire shape
`internal/plugins/process` transports over a plugin's stdio (see that
package's `types.go` — `Notification{JSONRPC, Method, Params}` — and
`transport.go`'s `routeFrame`). They record what a real plugin process
sends when it calls `host_http_request` and `host_secret_ref`
respectively.

- Tool: hand-authored to the exact `process.Notification` JSON shape (no
  external RPC client was used to generate them — the shape is the
  package's own wire contract, the same convention
  `internal/plugins/process/testdata/fixtures/README.md` documents for
  its own handshake frames).
- Version: JSON-RPC 2.0, `cascade.plugin` host ABI v1
  (05-PEWS-PLAN-W4-W6.md §Epic O preamble's host function list).
- Date captured: 2026-09-14.

`TestHostBoundary_RealPluginABI` (`boundary_integration_test.go`, build
tag `integration`) drives `plugin_abi_http_call.json` through a real
spawned `process.ProcessRuntime` — the fake plugin process writes this
exact frame to its stdout — and asserts the frame is denied at the real
boundary when the target host is outside the launched plugin's declared
net scopes. `plugin_abi_secret_ref.json` is exercised the same way for
`host_secret_ref` against a plugin holding no secret grant.
