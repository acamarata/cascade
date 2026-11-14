# MCP RPC methods

Methods the daemon serves for the MCP bridge. Access classes are listed in the
generated RPC method reference.

### `mcp.dispatch`

Carries one MCP request frame over the daemon socket, so MCP needs no second
listener. Params are the MCP JSON-RPC 2.0 frame itself. The handler parses the
frame, hands it to the MCP dispatcher and returns the encoded MCP response
after it has passed the egress marshaler. Registration is skipped on Windows,
where no daemon runs.

The call succeeds at the RPC layer even when the MCP request fails: an
unparseable frame, an unknown method or tool, and bad tool params are reported
inside the returned MCP response's `error` field. Only a failure to build the
response marshaler, or to marshal the response, is an RPC error.

### `mcp.capability.check`

Asks whether the calling agent holds an MCP capability.

Params (required, one object, unknown fields refused): `capability`, which
must start with `mcp.profile.` or `mcp.tool.`; anything else is
`invalid-input`.

Result: `{"verdict", "level", "auto_advance"}`. `verdict` is `allow` or `deny`;
every outcome that is not an allow is reported as `deny`. `auto_advance` is
true only for an allow that the policy marks as auto-advancing. A caller with
no session principal is denied at the strictest level without consulting the
policy engine. A daemon with no policy engine answers `unavailable`.
