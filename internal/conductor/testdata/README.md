# internal/conductor/testdata

## Fixture provenance (P1-E11-W3-S23-T3)

`stream_integration_test.go`'s `TestStreamRealSSE` uses no static fixture
files. Its "real counterpart" (Art.2) is produced live, in-process, for
every test run:

- a real `*events.Bus` (`internal/events`), backed by a real in-memory
  `provider.Store` (`internal/storage/storetest.NewMemStore()`);
- a real `internal/rpc.SSEHandler` (`GET /events`) bound to that bus;
- a real `internal/rpc.Registry`/`Handler` (`POST /rpc`), with
  `job.cancel` mounted through this package's own `RegisterHandlers`
  (`cancel.go`);
- a real unix-domain socket (`net.Listen("unix", ...)` under
  `os.MkdirTemp`) serving both routes via `internal/rpc.NewHandlerWithSSE`;
- a real `*http.Client` dialing that socket, issuing an actual HTTP/1.1
  `GET /events?filter=job:<id>` request and reading the server's real,
  chunk-encoded `text/event-stream` response body, plus a real
  `POST /rpc` JSON-RPC 2.0 exchange for `job.cancel`.

No SSE framing, JSON-RPC envelope, or event-bus wire format is
hand-authored by this test: every byte on the wire is produced and parsed
by the same `internal/rpc`/`internal/events` code the daemon composition
root uses in production. This directory holds no other fixture files for
this ticket.
