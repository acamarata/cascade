# Tool caller identity

The MCP conversation adapters require a registry-stamped `principal.Caller`.
Missing callers refuse `KindPermissionDenied` with `cascade-pa tool call has
no caller`. A context principal refuses with `cascade-pa tool call cannot
forward its principal`, regardless of the process environment. The context
principal cannot be assumed to match the process credential.

Every tool adapter client is built through `modelFacingClient`, after
`requireToolCaller`. The helper calls `client.New(...).ModelFacing()`. With
`CASCADE_PRINCIPAL_TOKEN` present, the client sends only `Cascade-Principal`.
Otherwise it sends `Cascade-Client: owner-model`. Even a context Caller with
`OwnerCLI: true` cannot change that marker. The daemon resolves `owner-model`
to a present Caller with no principal and `OwnerCLI: false`; conversation
handlers exclude local-only threads and enforce principal scope.

Both the conversation adapter and the bridge.send decorator follow this
rule. In-chat, soul, review, topic and pacmd CLI adapters retain `client.New`
and the owner-CLI marker. The two markers are exact, case-sensitive values.
Invalid or repeated markers refuse; a marker plus a token also refuses.

For P2-CPA-03, P2-CPA-06, P2-CPA-09 and P2-CPA-11: any adapter injected into
a `cascadepa.Set*` seam whose parameter comes from the tools package must
check `requireToolCaller` before its first RPC and construct clients only
through `modelFacingClient`. This includes future document and connector
adapters. Keep visibility decisions in the daemon. Unknown or omitted send
sensitivity resolves to local-only, which bridge mirroring refuses.

This narrows model-facing authority within the accepted ADR-0006 section 3
threat model. Same-UID code can still send `owner-cli`; defending against
that requires OS isolation.
