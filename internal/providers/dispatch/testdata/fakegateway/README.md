# fakegateway

A loopback-only stand-in for an Anthropic-compatible gateway, used by
`TestGatewayAcceptanceFake` in `cmd/cascade`. The test builds it with
`go build`, starts it with a per-test token in an environment variable,
and reads the address from its first stdout line
(`listening http://127.0.0.1:<port>`).

```
fakegateway -control DIR -journal FILE [-addr 127.0.0.1:0] [-token-env FAKEGW_TOKEN]
```

It refuses any address that is not a loopback IP.

## Provenance

No live capture exists. The gateway capture recorded in
`providers/anthropic/testdata/gateway/README.md` could not reach a gateway, so
**every behaviour below is constructed** from the public Messages API
reference, with synthetic values (`model-a`, `model-b`, `msg_000`,
`req_000`). Nothing here claims to match a real gateway byte for byte.

| Route | Constructed behaviour |
|---|---|
| `GET /v1/models` | two models, `model-a` and `model-b`, in the list shape with `has_more: false` |
| `POST /v1/messages` | a JSON message whose text is `fake gateway reply ok`; with `"stream": true`, the SSE sequence `message_start` .. `message_stop` |
| `GET /usage` | the usage source: `{"five_hour":{"used":75,"limit":200,"utilization":37.5,"resets_at":<now + 2 h, RFC 3339>}}` |
| any other path | 404 with the API error body |
| a request without the token | 401 `authentication_error` |

The token is accepted as `Authorization: Bearer <token>` or `x-api-key`.

## Control files

Both are read on every request, so a test can change them while the fake runs.

| File | Content | Effect |
|---|---|---|
| `DIR/faults` | `ok` (default) | every message succeeds |
| | `<N> x <shape> then ok` | the next N message requests fail with shape, then succeed |
| | `always <shape>` | every message request fails with shape |
| `DIR/usage` | `ok` (default) or `fail` | `fail` makes `/usage` answer 503 |

A changed `faults` script restarts its count. Shapes (all transient under
`docs/conductor/transient-retry.md`):

| Shape | Response |
|---|---|
| `overloaded` | 529 with an `overloaded_error` body |
| `api_error` | 500 with an `api_error` body |
| `stream_error` | 200 `text/event-stream` whose first event is an `overloaded_error` |

An unknown shape answers 400, so a mistyped script fails loudly.

## Journal

One JSON line per request: `method`, `path`, `status` and `bearer_matched`.
It never holds a header, a body or the token.
