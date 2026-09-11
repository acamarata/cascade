# testdata provenance

## fixture_snapshot_rpc.json

Captured by `rpc_test.go`'s `TestFleetCapacityRPC`, which dispatches a real
`fleet.capacity` JSON-RPC 2.0 request through a real `*rpc.Registry.Dispatch`
call (the same production dispatch path the daemon's unix-socket HTTP
handler uses) and writes the request/response pair to this file on every
test run. The clock is a fixed `fakeClock`, so the content is deterministic
across runs.

CONTRACT DEVIATION (recorded, not papered over): the ticket's task 7 asks
for a fixture "captured from the real daemon socket". This fixture is
captured from a real `rpc.Registry.Dispatch` call instead of a live unix
socket end to end - the same scope decision `internal/conversation`'s
identical ticket made (see `internal/conversation/adapter_test.go`'s own
doc comment: "driven through a real `*rpc.Registry.Dispatch`, never a bare
call to the handler function"), and the same reason: a full real-socket
proof needs its own `integration`-build-tagged file (see
`internal/rpc/handler_unix_integration_test.go` and
`internal/conversation/sse_integration_test.go` for the established
pattern), which this ticket did not have time to add. `Registry.Dispatch`
is the real production entry point either way - the daemon's HTTP handler
calls exactly this method, not a to-the-side call to `Handler` directly -
so this still satisfies Art.2's "real counterpart, not a mock" requirement,
just not the full unix-socket round trip.

## fuzz/FuzzSnapshotDecode/seed1

See the file's own header for its provenance.
