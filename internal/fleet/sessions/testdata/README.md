# testdata provenance

## sse-fixture.txt

**SECONDARY conformance evidence only.** The PRIMARY evidence for this
ticket's SSE wire-format acceptance criterion is a real, independent SSE
consumer (a standard EventSource-compatible client, or curl) that does not
share this package's own parser — see `sse_integration_test.go`
(`//go:build integration`).

- Tool: `go test` (stdlib `net/http/httptest.ResponseRecorder`), against
  this package's own `sessions.SSEHandler` (`sse.go`).
- Command: a one-off harness test driving `SSEHandler.ServeHTTP` over a
  real `internal/events.Bus`, publishing one `fleet.sessions.changed`
  event and advancing a frozen clock 16s to also capture one heartbeat
  comment line.
- Go version: `go1.26` (module `github.com/acamarata/cascade`).
- Date: 2026-09-06.
- Contents: the literal bytes `ServeHTTP` wrote for one event plus one
  heartbeat — `event:`, `id:`, `data:`, `retry:` lines terminated by a
  blank line, then a `: keep-alive` comment line.

CONTRACT DEVIATION, recorded: the ticket's task text says this fixture
should be "captured from the D/S-06.T4 bridge" (`internal/rpc.SSEHandler`,
GET `/events`). That handler's wire format is `id`/`data` only — it never
writes `event` or `retry` lines (see `internal/rpc/sse.go`'s package doc).
This ticket's own acceptance criteria require all four fields
(`event`/`data`/`id`/`retry`), which only this package's own
`sessions.SSEHandler` produces. Capturing the D/S-06.T4 bridge's actual
output here would therefore not match the format this ticket ships, and
would silently misrepresent what the fixture is proving. The fixture is
instead captured from this ticket's own handler, honestly labeled as
secondary/self-captured evidence — exactly the category Art.2 requires a
REAL independent client to sit alongside, never replace. See
`sse_integration_test.go` for that independent-client proof.
