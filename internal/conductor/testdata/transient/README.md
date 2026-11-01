# Transient-failure fixtures

Source capture: `providers/anthropic/testdata/gateway/README.md`
(P1-GWY-01, contract:gateway-wire-fixtures). That finding records that no
live capture exists: no transient shape was observed. Every file here is
therefore **constructed**, not copied from a capture. Each follows the
public Messages API error envelope and SSE event shapes and uses only
synthetic values (`model-a`, `msg_000`).

| File | Status | Label | Expected class |
|---|---|---|---|
| transient_500.json | 500 | constructed | transient, `http_5xx` |
| transient_503.json | 503 | constructed | transient, `http_5xx` |
| transient_529.json | 529 | constructed | transient, `overloaded` |
| transient_stream_api_error.sse | 200, then an SSE `error` event (`api_error`) before any delta | constructed | transient, `stream_api_error` |
| transient_stream_overloaded.sse | 200, then an SSE `error` event (`overloaded_error`) before any delta | constructed | transient, `stream_api_error` |
| unauthorized_401.json | 401 | constructed (stands in for the 401 capture) | not transient |

The status is not stored in the body; the file name carries it and the
tests (`providers/anthropic/transient_test.go`) serve each body under it.
Other statuses in those tests (502, 504, 520 to 524, 501, 505 and the 4xx
set) are constructed inline with the same envelope.

When an operator names a reachable gateway and the P1-GWY-01 capture is
redone, each observed transient shape replaces its constructed file here,
scrubbed by the same rules, and this table records it as captured.
