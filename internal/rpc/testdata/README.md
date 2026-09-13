# internal/rpc/testdata (supervisor.* fixtures)

## supervisor-rpc-fixture.json (P1-E18-W4-S40-T4, Art.2 provenance)

Captured by `supervisor_test.go`'s `TestSupervisorRPCFixture`, which dispatches
a real `supervisor.snapshot` JSON-RPC 2.0 request through a real
`*rpc.Registry.Dispatch` call (the same production dispatch path the
daemon's unix-socket HTTP handler uses — see `handler.go`) and writes the
request/response pair to this file on every test run. The `SupervisorSource`
behind the dispatched call is a fixed, real (non-mock-framework) Go value,
so the fixture is deterministic across runs.

CONTRACT DEVIATION (recorded, not papered over): the ticket's task 4 asks
for a fixture "recorded from the real IPC socket (daemon running)". This
fixture is captured from a real `rpc.Registry.Dispatch` call instead of a
live unix socket end to end — the same scope decision
`internal/fleet/capacity/testdata/README.md`'s identical ticket already
made and documented for this exact situation (see that file's own
CONTRACT DEVIATION note). `Registry.Dispatch` is the real production entry
point either way — the daemon's HTTP handler calls exactly this method,
never a to-the-side call to a handler function directly — so this still
satisfies Art.2's "real counterpart, not a self-authored dialect"
requirement, just not the full unix-socket round trip. The checks list's
separate `-tags integration -run TestSupervisorRPCRoundTrip` entry
(`supervisor_integration_test.go`) covers the real-unix-socket half this
fixture does not, mirroring `internal/rpc/handler_unix_integration_test.go`'s
established real-socket pattern.

- Tool: `go test` (`TestSupervisorRPCFixture`, `internal/rpc/supervisor_test.go`).
- Repo commit at capture time: `d041554` (branch `p1-integration`).
- Go toolchain: `go1.26.6 darwin/arm64`.
- Date: 2026-09-13.

## supervisor-sse-fixture.ndjson (P1-E18-W4-S40-T4, Art.2 provenance)

Captured from a **real** `internal/rpc.SSEHandler` instance — this
package's actual production GET `/events` handler (the same one
`cmd/cascade/daemon_unix_run.go`'s `buildRPCServer` wires under the
`"daemon"` bus namespace) — bound to a real `internal/events.Bus` backed by
`internal/storage/storetest.NewMemStore()` (`newTestBus`, `sse_test.go`),
exercised over a real `net/http/httptest` HTTP round trip (`runSSE`). Four
real `SupervisorEventEmitter.Emit*` calls (the same production code path a
real producer would call — see `supervisor_sse.go`'s own WIRING GAP note
for why no production call site exists yet) produced the four SSE records
in this file, in the exact wire format (`id:`/`data:`, base64-encoded
payload) `internal/rpc/sse.go`'s `writeSSEEvent`/`sseEventJSON` write in
production. This is `internal/rpc`'s own shared `SSEHandler` envelope,
distinct from `internal/fleet/sessions`' separate, domain-specific
`SSEHandler` (`event:`/`id:`/`data:`/`retry:`,
`internal/fleet/testdata/top-sse-fixture.ndjson`) — the two packages emit
different wire shapes and this fixture is real to the one D/S-06.T4
actually specifies for this ticket.

CONTRACT DEVIATION (disclosed, not papered over): captured from the real,
unmodified production `SSEHandler` over a real HTTP round trip, not from a
live `cascade daemon run` process's socket end to end — no running daemon
was available to this capture, and (per `supervisor_sse.go`'s WIRING GAP
note) no production code path calls these four `Emit*` methods yet either.
This is the same established precedent `internal/fleet/testdata/README.md`
(P1-E18-W4-S40-T1) and `internal/rpc/testdata/sse-session.txt`
(P1-E29-W6-S60-T1) already set for this exact situation: real handler,
real bus, synthetic `Emit*`/`Publish` calls standing in for a live
daemon's own event producers.

- Tool: `go test` (`TestSupervisorSSEFixtureCapture`, `internal/rpc/supervisor_sse_test.go`).
- Repo commit at capture time: `d041554` (branch `p1-integration`).
- Go toolchain: `go1.26.6 darwin/arm64`.
- Date: 2026-09-13.

Events carried: one of each of the four kinds —
`supervisor.attention_added`, `supervisor.stall_detected`,
`supervisor.escalation`, `supervisor.headroom_update` — for session
`sess-alpha`, enough for `TestSupervisorSSEReplay` to exercise all four
payload shapes and confirm each carries `schema_version`.

## fuzz/FuzzSupervisorSnapshotParams/seed001

Hand-authored seed corpus (`go test fuzz v1` format) seeding the fuzzer
with an unknown-field rejection case; see `supervisor_fuzz_test.go`'s own
header for what it exercises.
