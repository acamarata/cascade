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

## Caller headers and access classes

After the local guard, both routes resolve `Cascade-Principal` as an opaque,
daemon-issued credential, or `Cascade-Client: owner-cli` as the interactive
owner marker. Both headers together refuse with HTTP 400. A token carried
only in a query string has no authority. The SDK sends the principal header
when `CASCADE_PRINCIPAL_TOKEN` is present, suppressing the owner marker even
if that environment value is empty. Invalid tokens refuse with HTTP 403;
the public message is `caller principal not recognized` for unknown,
expired, revoked and pre-restart credentials alike.

Credentials are memory-only. A daemon restart invalidates every token;
launchers must mint replacements in the new daemon. No credential is
persisted, logged or placed in an audit row. Refusal reasons are audited
without the token. Tokens expire after at most 24 hours.

The registry checks an explicit access declaration before middleware:

| Class | Admission |
| --- | --- |
| Unmarked | Internal error; no handler or middleware runs |
| Open | Socket guard suffices |
| OwnerOnly | Transport-stamped Caller with no principal |
| OwnerView | Stamped owner or principal; handler must scope principal reads |
| Scoped | Principal required; owner marker alone refuses |
| Ingest | Write-only; no principal required, no stored data returned |

Unknown class values refuse with `unknown access class`. An absent Caller
never receives the owner view. `OwnerCLI` changes no access class; it marks
the CLI for local-only content. Chat, memory, recall and fleet reads remain
OwnerOnly until their handlers enforce principal scope. `mcp.dispatch` is
OwnerView; inner tool calls need their own method checks.

The default event stream and `fleet.sessions` are OwnerOnly. Undeclared
topics refuse. OwnerView topic handlers must implement `EventVisibleConsumer`
and apply the context predicate per event. Every principal stream receives
a predicate, defaulting to deny-all until the ancestry factory is installed.

This protects model-facing surfaces from cross-session leakage. It is not
an isolation boundary against shell-capable code running under the same UID;
that requires OS isolation. See [caller principal](security-posture/caller-principal.md).

## POST /rpc

Standard JSON-RPC 2.0 over HTTP. The request body is a JSON-RPC request
object; the response is always HTTP 200 with a JSON-RPC response envelope
in the body (errors are reported inside the envelope, not via the HTTP
status line). A `client_version` field on the request triggers a
version-skew check before dispatch.

## GET /events

Bridges an internal event bus to a subscribed client over Server-Sent
Events, per the W3C Server-Sent Events Living Standard.

### Topic dispatch

`GET /events` is a topic-dispatching mux (`internal/rpc.SSEMux`), keyed by
the `topic` query parameter:

```
GET /events                       # no topic -> the daemon-wide stream below
GET /events?topic=fleet.sessions  # -> the fleet.sessions SSE stream
```

Before any of this, the request passes the same local-request guard as
`POST /rpc`: a peer that is not the daemon owner, or a browser-shaped
request (an `Origin`, `Sec-Fetch-Site` or `Sec-Fetch-Mode` header, a non-socket `Host`), gets
HTTP 403 and no topic is ever looked at.

A request whose query string carries no key naming a topic parameter, by
any casing or padding, serves the daemon-wide stream this section
documents; an unrelated query key belonging to that stream (`filter`,
below) or to a caller's own convention is none of this mux's business
and does not change that. A request with exactly one `topic` value,
under the byte-exact key `topic`, that names a registered stream, byte
for byte after percent-decoding (today: `fleet.sessions`, the fleet
sessions domain's own `event`/`data`/`id`/`retry` SSE format; see the
Fleet CLI wiki page), serves that stream instead, with its own filter
and format rules. **Everything else is refused with HTTP 400 before
either stream's subscribe logic runs**: an unknown topic, a repeated
`topic` parameter (`?topic=fleet.sessions&topic=x`, in either order), an
empty value (`?topic=`), a query string that does not parse strictly (a
bad `%` escape or a `;` separator), and any key that only *looks* like
`topic` rather than being it — a case variant (`Topic`, `TOPIC`), a
padded one (`+topic` or `%20topic`, which decode to a leading space), or
a second such key alongside the real one (`?topic=fleet.sessions&Topic=x`).
A topic the mux does not recognize, or a topic key it cannot resolve to
exactly one value, is never served the daemon-wide stream. The 400 body
is a JSON-RPC-shaped envelope (`{"jsonrpc", "error": {"code", "message",
"data": {"kind": "invalid-input"}}, ...}`), the same shape `POST /rpc`
errors use, with a `message` drawn from a small fixed set of strings —
it never echoes the query string or any topic value back to the caller,
so a hostile or oversized topic never reaches whatever reads the
refusal.

### Request

```
GET /events?filter=<comma-separated-event-types>
Last-Event-ID: <opaque-resume-token>
```

Both the query parameter and the header are optional (and apply to the
daemon-wide stream above; a topic-routed stream may define its own query
parameters — `fleet.sessions` currently defines none beyond `topic`
itself).

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

## status.get

`status.get` returns a one-shot snapshot of the daemon process: `version`,
`daemon` (process id, uptime, connection count and socket), `health`, and
`subsystems`. It also carries a `hooks` block:

```json
{"hooks": {"unknown_event_drops": 0}}
```

`hooks.unknown_event_drops` is the number of hook events the daemon dropped
because their event type is not one it recognises. The hook handler drops
those events without failing the harness, so this counter is the only place
a harness newer than the daemon shows up. It is always present, counts since
the daemon process started, and `cascade doctor` turns a non-zero value into
a `hook-events` warning.

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

## chat.create_thread

`chat.create_thread` starts a new conversation thread. The daemon mints the
thread id. In one database transaction it writes the thread row, the
thread's privacy marker and the thread's attribution. Clients never name a
new thread's id, and there is no method that changes a marker later.

Access class: **owner-view**. The owner CLI's threads are attributed to
`owner`. A principal's threads are attributed to the principal's own project
scope, and only that principal's scope can read them. A call with no caller
identity is refused with `permission-denied` before anything is written, and
so is a principal without a project scope.

Params (required):

```json
{"privacy_mode": "internal"}
```

`privacy_mode` must be exactly one of `local-only`, `restricted`, `internal`
or `public`. A missing, empty or differently spelled mode, an unknown field
or trailing data is refused with `invalid-input`, and nothing is written.

Result:

```json
{"thread_id": "thread-<daemon-minted id>"}
```

Errors:

- `invalid-input`: the params above are malformed.
- `permission-denied`: no caller identity, or a principal without a project
  scope.
- `conflict`: the minted id already exists. Nothing is written.
- `unavailable`: the store failed. The transaction rolls back, so no thread,
  marker or attribution is left behind.

**Privacy markers are write-once.** Once a thread has a marker, writing the
same mode again is a no-op and writing a different mode is refused with
`conflict`, leaving the marker unchanged. A thread that already holds turns
and has no marker keeps its implicit `restricted` mode: setting any marker on
it is refused with `conflict`.

**Telegram bridge threads.** The bridge creates every new chat thread with
`chat.create_thread` at `internal`, and remembers it in a durable
chat-to-thread map in the bridge's own state. Only the paired owner's direct
chat (a private chat whose id is the sender's id) is ever mapped; groups and
channels never get a thread. A chat whose legacy `bridge-telegram:<chat id>`
thread already exists is bound to that thread instead, so its history
carries over. An adopted legacy thread keeps whatever marker it had. If it
had none, it stays unmarked and reads `restricted`: adoption never widens it,
and messages on it are not mirrored to Telegram in either direction. Any map
error refuses the message: nothing is appended and nothing is sent.

A legacy `bridge-telegram:<chat id>` thread that has turns and no marker is
adopted as it is. It reads `restricted`, so it is neither recorded from nor
mirrored to. Setting a marker on a thread that already holds turns is refused, so this cannot be changed in place. The
chat stays bound to that thread until an owner-only rebind exists, which is
not part of this release.

## chat.append_turn: thread targets

`chat.append_turn` appends one turn. The daemon mints every thread id. A
request with no `thread_id` starts a new thread: in one transaction the
daemon writes the thread row, its privacy marker (the request's
`privacy_mode`, or an explicit `restricted` when it names none), the turn and
its segments. A refused request leaves no thread, marker or turn (a creating request can leave
the `thread_scope` attribution row of its unused id; the row grants nothing). A request
with a `thread_id` continues that thread, which must already exist
(`not-found` otherwise) and may not carry a `privacy_mode` (`invalid-input`).

An optional `leading_context` string is stored as segment 0 of the appended
turn, ahead of `segments`. The client sends a resumed block this way, once.

## chat.resume

`chat.resume` composes a recorded conversation into a chat session as the
leading block of the session's next turn. It writes nothing and returns the
same block for the same params.

Access class: **owner-view**. The owner resumes between any threads. A
principal resumes only between threads attributed to its own project scope:
a foreign `session_thread_id` is refused with `permission-denied`, and a
foreign source thread reads as `not-found`.

Params:

```json
{"session_thread_id": "<existing thread>", "topic_id": "<thread id>", "budget": 4000, "cwd": "/path"}
```

`session_thread_id` is required and must name an existing thread; its stored
privacy marker is the target tier. Exactly one of `topic_id` or `query` names
the source. `budget` and `cwd` are optional. An unknown field is refused with
`invalid-input`.

Result:

```json
{"thread_id": "<primary source thread>", "leading_context": "<block>", "tokens": 120, "trimmed": []}
```

A source whose tier is more restrictive than the target
(`local-only` > `restricted` > `internal` > `public`) is refused with
`policy-denied` and nothing is returned. A query resolves through
`recall.what`; its conversation results are composed, and the block's tier is
the strictest of their markers.

## bridge.send

`bridge.send` mirrors one turn that is already committed in a conversation
thread to the owner's paired Telegram chat. The `cascade_cpa_send` tool calls
it after a successful `chat.append_turn`, and only for a chat-bridge thread.
Only the daemon process holds the live bridge, so the tool reaches it over the
daemon socket.

Access class: **owner-only**. A session or other principal token is refused
with `permission-denied` before the handler runs, so nothing reaches the
transport.

Params:

```json
{"thread_id": "<thread id>", "content": "<turn text>", "sensitivity": "internal"}
```

`sensitivity` is the turn's resolved sensitivity (`public`, `internal`,
`restricted` or `local-only`). An empty value is `restricted`; an unknown
value is refused.

Result:

```json
{"mirrored": true}
{"refused": true, "reason": "turn sensitivity is not internal or public"}
{"reason": "the thread is not a chat-bridge thread"}
```

- `mirrored`: exactly one Bot API send was made.
- `refused`: the turn's sensitivity is narrower than `internal`. The daemon
  publishes one content-free `bridge.refused` event and makes no transport
  call.
- Neither flag, with a `reason`: nothing was sent because the daemon runs no
  enabled chat bridge, or the thread is not a chat-bridge thread. With no
  bridge wired, only a legacy `bridge-telegram:<chat id>` id gets the
  no-bridge reason; any other id reads as not a chat-bridge thread.

The send itself also enforces the thread's own sensitivity tier, so the
message goes out only when both the turn and the thread are `internal` or
`public`. A chat-bridge thread is one the bridge's chat-to-thread map holds
(see `chat.create_thread`); the thread id's shape decides nothing, and a
thread the map does not hold is skipped with zero transport calls. The
mapped chat must be the current paired binding's chat. An unpaired bridge,
another chat's thread, a binding-store error and a privacy-resolver error all
refuse with `policy-denied`, one `bridge.refused` event and zero transport
calls. A map read error is returned as an error with zero transport calls. A
transport error returns to the caller as an error and emits no
`bridge.refused`.

Once the daemon starts stopping the bridge, `bridge.send` returns
`unavailable` (`-32004`) with no transport call. Shutdown order is: refuse new
calls and wait for admitted ones, drain the chat bridge, then stop the module.
