# Daemon RPC protocol

The daemon exposes two routes over one HTTP/1.1 server bound to a local
unix socket: `POST /rpc` for JSON-RPC 2.0 calls, and `GET /events` for a
Server-Sent Events (SSE) stream of daemon events. Both routes run behind
the same local request guard, described below: only the socket owner may
connect, and only from a request shaped like a first-party local client.

## Local request guard

`internal/rpc`'s `guardLocalRequest` (`request_guard.go`) runs for both
`POST /rpc` and `GET /events`, before any JSON-RPC parse, SSE subscription
or handshake. It checks, in order:

1. **Owner-only.** The connection's peer credential (resolved once per
   accepted connection by `ConnContext`, the same mechanism both routes
   have always used) must resolve and its UID must equal the daemon's own
   UID. An unresolved peer credential or a non-owner UID is refused —
   fail-closed, never "unknown means allow."
2. **No browser shape.** The request must not carry any marker of having
   reached the socket from a browser rather than a first-party local
   client: an `Origin` header (any value, including the literal `"null"`
   browsers send for opaque origins); a `Sec-Fetch-Site` or
   `Sec-Fetch-Mode` header (browsers send these on same-origin and
   no-cors requests too, where `Origin` can be absent); a `Host` header
   other than exactly `unix` or `cascade.sock` (the two values every
   first-party client sends today — the CLI SDK, hook packs, the node
   heartbeat tunnel sender, and the macOS widget's `Network.framework`
   transport all send one of these two, never `localhost`, which is what
   a browser reaching a loopback TCP bridge, e.g. `ssh -L`/`socat`, or a
   DNS-rebinding page would send instead); or, on a `POST`, a
   `Content-Type` that does not parse to media type `application/json`
   (absent, unparsable, `text/plain`, form or multipart data all refuse —
   a parameter such as `charset=utf-8` is accepted).

A refused request receives a plain HTTP `403` with a fixed body —
`forbidden: socket peer is not the daemon owner` or `forbidden:
browser-shaped request refused` — never a JSON-RPC error envelope and
never an echo of anything from the request itself. Nothing past the guard
runs for a refused request: no body read, no JSON-RPC `Parse`, no SSE
subscription, no `text/event-stream` header.

This guard covers the local unix socket only (the daemon's own socket and
`cascade mcp serve --socket`'s dedicated socket both run the same
`internal/rpc.Handler`/`ConnContext` pipeline). A remote (network)
listener is out of scope here; see the remote-transport work for that.

## POST /rpc

Standard JSON-RPC 2.0 over HTTP. The request body is a JSON-RPC request
object; the response is always HTTP 200 with a JSON-RPC response envelope
in the body (errors are reported inside the envelope, not via the HTTP
status line). A `client_version` field on the request triggers a
version-skew check before dispatch.

## GET /events

Bridges an internal event bus to a subscribed client over Server-Sent
Events, per the W3C Server-Sent Events Living Standard.

### Request

```
GET /events?filter=<comma-separated-event-types>
Last-Event-ID: <opaque-resume-token>
```

Both the query parameter and the header are optional.

**filter**: a comma-separated list of event type names. Whitespace around
each name is trimmed. An empty or absent filter subscribes to every event
type. An unrecognized type is rejected with HTTP 400 before the SSE
handshake starts; the response body names the rejected type, and no SSE
headers or body are written.

**Last-Event-ID**: an opaque, base64url-encoded resume token, taken
verbatim from a previous event's `id:` line. If present and well-formed,
the stream replays events starting immediately after that position. If
absent, malformed, or unrecognized, the connection opens at the current
tail instead of returning an error, per the SSE specification's SHOULD
semantics for this header. A client should always send back the last
`id:` value it saw, unmodified.

### Response

On success, the response carries:

```
Content-Type: text/event-stream
Cache-Control: no-cache
Connection: keep-alive
```

followed by a stream of records. Each event is written as:

```
id: <resume-token>
data: <json-payload>

```

`data` is a JSON object with `seq`, `kind`, `source`, and `payload`
(base64-encoded) fields.

If no event has been sent for 15 seconds, the server writes a comment
line to keep the connection alive through idle-timing proxies:

```
: keep-alive

```

The connection stays open until the client disconnects or the server
closes it after an unrecoverable delivery error.

### Platform support

`GET /events` depends on the daemon's unix socket IPC layer, which is not
available on Windows. A Windows client receives HTTP 501 with a message
explaining the limitation.

## supervisor.* methods and events

The `supervisor.*` namespace is reserved for future product consumption
(a dashboard). It is implemented in `internal/rpc` (`supervisor.go`,
`supervisor_sse.go`) and reserves no core storage domain of its own.

**`supervisor.snapshot`** (no params) returns the current fleet
supervision snapshot: `schema_version`, `attention_queue_depth`,
`stall_count`, one `sessions[]` entry per known session
(`session_id`, `action_count`, `interrupt_count`), `headroom_ceiling`
(nullable), `autonomy_profile`, and `auto_advance_tier`. With no daemon
supervision surface running, it returns a taxonomy `unavailable` error
(JSON-RPC `-32004`, CLI exit `5`) rather than a panic or a zero-value
result.

**`supervisor.events_schema`** (no params) returns the versioned schema
doc describing the four SSE event payloads below.

Four typed SSE events are published on the same `GET /events` stream
above (under the `"daemon"` bus namespace, so they are visible without an
extra subscription): `supervisor.attention_added`,
`supervisor.stall_detected`, `supervisor.escalation`, and
`supervisor.headroom_update`. Every payload carries `schema_version`; see
`supervisor.events_schema`'s response for the full per-kind field list, or
`internal/rpc/testdata/supervisor-sse-fixture.ndjson` for a captured
example of all four.

## status.widget and status.widget_changed

`status.widget` is implemented in `internal/daemon` (`status_widget.go`,
`status_widget_sse.go`, `status_widget_jobs.go`) over its own
`internal/fleet/capacity.Compositor` instance. It is registered on the
same method registry every other daemon RPC is, so it is dispatched only
after the socket-peer-UID check above refuses a non-owner connection.

**`status.widget`** takes optional params `{"scope": <ScopeRef>}` (a
`ScopeRef` is `{"kind": "...", "id": "..."}`, `internal/context/scope.Ref`
under the hood — there is no separate `SessionScopeRef` type). When
`scope` is omitted, the daemon has no per-connection session identity to
resolve one from (the peer-UID check above is a single-owner boolean, not
a session/task/project scope — see `internal/fleet/supervision`'s own
`fleet.attention.list`, whose `own_scope` param is REQUIRED for the same
reason), so it defaults to the global scope kind.

The response is a `WidgetSnapshot`:

| Field | Type | Notes |
|---|---|---|
| `rows` | `WidgetRow[]` | one per provider profile/pool |
| `attention_count` | int | unacked items in the resolved scope |
| `active_jobs_count` | int or `null` | `null` only when the daemon has no jobs domain — never a fabricated 0 |
| `nodes` | `NodePresenceSummary[]` | `{id, presence, reachable, trust_tier}` |
| `projects` | `ProjectRow[]` or `null` | always `null` today — no production source populates it yet |
| `generated_at`, `seq` | | `seq` is the same monotonic counter `status.widget_changed` uses |

Each `WidgetRow` carries `ref` (opaque stable id), `label` (neutral
display label), `kind`, `five_hour`/`seven_day` (`{utilization_pct,
resets_in}`, both fields `null` together when the source is stale or
absent — never a fabricated zero), `state`, `reauth_required`, and
`updated_at`.

**Redaction (always applied, before the response leaves the daemon):**
every `ref`/`label`/`id` field is scanned for five PII pattern families
(email, remote URL, absolute path, hostname, `@handle`); a match replaces
`ref`/`id` with a short stable hash and `label` with the literal
`"redacted"`. Independently, `[widget].show_project_names` (see
`config-reference.md`) controls whether `projects[].label` carries the
real project label or the stable neutral form `"Project 1"`, `"Project
2"`, ... — the PII scan above still applies on top of either choice.

**`status.widget_changed`** is an SSE event on the same `GET /events`
stream, under the `"daemon"` namespace (the one namespace the daemon's
real `/events` endpoint subscribes to — an event published under any
other namespace is invisible to it, a mistake `fleet.capacity_changed`
makes today). It fires on two real triggers: a material change in the
underlying `capacity.Compositor` snapshot, and a push or ack through
status.widget's own attention-queue reader. The payload is a full,
redacted `WidgetSnapshot` with an incrementing `seq` for client dedup
(small enough that a delta encoding is not needed).
