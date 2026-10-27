# Caller principals

The daemon admits the local socket owner first and rejects browser-shaped
requests before looking up credentials. A launcher can mint a random
credential bound to a session, MCP server, hook, node job or enrolled remote
client. Plugin and node kinds cannot be minted. Scope must name a project;
personal/global scope is not a principal authority. Node jobs also bind an
envelope digest. Launchers replace inherited credentials with `BindEnv`.

Credentials live only in the issuing daemon's memory, indexed by SHA-256.
They are never persisted or included in logs, audit, events, errors or query
strings. Expiry, revocation and daemon restart all invalidate them. The
public refusal is identical; only audit rows distinguish expiry, revocation
and restart/unknown. A launcher re-mints after restart.

The admission threat model is model-facing cross-session leakage and
prompt-driven misuse. Same-UID shell code can forge the owner marker or read
another process's environment. OS isolation is required for that threat.
Doctor consumers use the principal status API; access-mark counts belong to
the RPC registry. A minter status does not claim any RPC markings.

Interim chat, memory, recall, context and fleet reads are owner-only until
their handlers filter by principal scope. Principals cannot call vault,
configuration, policy, approval, elevation or node administration. The owner
view is a transport-stamped Caller with no principal; `OwnerCLI` separately
identifies the interactive CLI for local-only content. An absent Caller
refuses. The MCP dispatcher is OwnerView, never Open.

## Caller inventory

| Caller | Required authority |
| --- | --- |
| SDK CLI | Owner marker, or principal token exclusively when inherited |
| Installed session hook commands | `fleet.sessions.hook_event`: Ingest, principal-less allowed |
| Legacy hookpack curl bodies | Ingest for hook-event; owner marker for owner-only reads |
| Completion and hydration command hooks | SDK headers; completion check is owner-only; hydration degradation is Ingest |
| Widget status transport | `status.get` is Open; `status.widget` requires an owner Caller |
| Plugin processes | Host handle; plugin token minting is refused |
| Node dispatch clients | Owner-only pending the dedicated node transport |
| `cascade mcp serve --stdio`, no token | Owner view from its own registry, like the owner CLI |
| `cascade mcp serve --stdio`, token set | Every declared method forwarded to the daemon with the token; refused when unreachable |
| `cascade mcp serve --socket` peer | Header-less or owner marker: owner, never `OwnerCLI`; a principal token is refused |

Launcher binding belongs to the agent, harness and node consumers. No model-facing mint RPC exists.
Default SSE and fleet-session topics remain owner-only. Future OwnerView
topics must consume the event-visibility predicate; its missing-factory
behavior is deny-all, including a factory returning nil.

## MCP servers

`cascade mcp serve` follows the CLI rule. A process with no
`CASCADE_PRINCIPAL_TOKEN` is the owner: it is exactly as trusted as the owner
CLI, because the same UID is not a boundary, and it serves tools from its own
registry. A process that carries the variable, even empty, is
principal-bearing. Every method in its registry that has an access
declaration is forwarded to the daemon with the token, and the daemon applies
the caller's scope. A method with no declaration refuses. When the daemon
cannot be reached each call refuses `KindUnavailable`; nothing falls back to
the in-process owner view. Builtin plugin tools and in-process core tools that
bypass the method registry are not listed in that mode. The token is read once
at start and bound into the daemon client, which never reads the environment
again: unsetting or changing the variable later cannot make a call the owner
CLI or another principal.

The MCP socket admits no principal. Its access policy has no minter, so a peer
presenting a token is refused before any handler runs. Real MCP clients send
no Cascade header, so a header-less peer and an owner-marker peer are both the
owner. On both transports every tool handler, builtin plugin tools included,
sees a Caller with no principal and `OwnerCLI` false.

The daemon's own `mcp.dispatch` runs each core tool's method through the
registry again, so a principal admitted to the dispatcher still meets the
inner method's class: an owner-only verb refuses. Builtin plugin tools and
the backup tools on the daemon's dispatcher do not pass through the registry,
so they are owner-only: a caller principal, or a call with no Caller, is
refused `KindPermissionDenied`.
