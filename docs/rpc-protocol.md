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
