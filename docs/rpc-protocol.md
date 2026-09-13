# Daemon RPC protocol

The daemon exposes two routes over one HTTP/1.1 server bound to a local
unix socket: `POST /rpc` for JSON-RPC 2.0 calls, and `GET /events` for a
Server-Sent Events (SSE) stream of daemon events. Both routes run behind
the same peer-UID ownership check: only the socket owner may connect.

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
