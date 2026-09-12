# internal/fleet/testdata

## top-sse-fixture.ndjson (P1-E18-W4-S40-T1, Art.2 provenance)

Captured from a **real** `internal/fleet/sessions.SSEHandler` instance —
this repo's actual production GET `/events?topic=fleet.sessions`
handler — wired to a real `internal/events.Bus` backed by
`internal/storage/storetest.NewMemStore()` (the same real-bus/real-handler
combination `internal/fleet/sessions/sse_test.go` itself uses), exercised
over a real `net/http/httptest.NewRecorder()` HTTP round trip. Three real
`bus.Publish` calls on the handler's own `fleet.sessions`/
`fleet.sessions.changed` namespace/kind produced the three SSE blocks in
this file, in the exact wire format (`event:`/`id:`/`data:`/`retry:`)
`SSEHandler.stream`/`writeSSEEvent` write in production.

CONTRACT DEVIATION (disclosed, not papered over): this is captured from
the real, unmodified production `SSEHandler` over a real HTTP round trip,
not from a live `cascade daemon run` process's socket end to end — no
running daemon was available to this capture. This is the same
established precedent `internal/rpc/testdata/sse-session.txt`
(P1-E29-W6-S60-T1) already set for this exact situation: real handler,
real bus, synthetic `Publish` calls standing in for a live daemon's own
event producers, which this ticket's own scope (`internal/fleet/top_*.go`
+ `cmd/cascade/fleet_top*.go`) has no wiring into.

- Tool: a throwaway `package main` (deleted after the capture; never
  committed) driving `sessions.NewSSEHandler` + `events.New` +
  `storetest.NewMemStore` + `httptest.NewRecorder`, mirroring
  `sse_test.go`'s `newTestSSEHandler`/`runSSE` helpers exactly.
- Repo commit at capture time: `79bc5cc` (branch `p1-integration`).
- Go toolchain: `go1.26.6 darwin/arm64`.
- Date: 2026-09-12.
- Clock: `testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))` — every
  `updated_at` in the fixture is a fixed offset from that instant, not a
  real wall-clock read, so the fixture (and any test built on it) stays
  byte-identical on re-capture.

Sessions carried: `sess-alpha` (claude, running -> completed) and
`sess-beta` (codex, stalled) — three events total, enough for
`TestFleetTopSSEReplay` to exercise an upsert (new row), an unrelated
second row, and a same-ID state transition.
