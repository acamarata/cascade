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
| In-process `cascade mcp serve` | Separate registry; daemon principal policy does not yet cover it |

The in-process MCP registry gap belongs to P1-SEC-38. Launcher binding belongs
to the agent, harness and node consumers. No model-facing mint RPC exists.
Default SSE and fleet-session topics remain owner-only. Future OwnerView
topics must consume the event-visibility predicate; its missing-factory
behavior is deny-all, including a factory returning nil.
