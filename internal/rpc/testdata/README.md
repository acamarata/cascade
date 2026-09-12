# internal/rpc/testdata provenance

## sse-session.txt

Art.2 real-counterpart fixture for the six P1-E29-W6-S60-T1 job/lease SSE
event kinds (`sse_jobs.go`).

- Tool: this repo's own `internal/rpc.SSEHandler` (`sse.go`), the real,
  unmodified D/S-06.T4 SSE server this ticket registers topics against —
  never a self-authored dialect.
- Capture method: a real `internal/events.Bus` (in-memory store,
  `internal/storage/storetest.NewMemStore`) published four real events
  (`job.leased`, `job.transitioned`, `job.completed`, `lease.acquired`),
  and `SSEHandler.ServeHTTP` was driven end-to-end over a real
  `net/http/httptest` request/response pair (the same `runSSE` harness
  `sse_test.go` uses for its own required unit tests). The response
  body's raw bytes were written verbatim to this file.
- Version: this repo's `internal/rpc` package as of commit range
  containing ticket P1-E29-W6-S60-T1 (P1, Epic AC, Wave 6, Sprint 60).
- Date: 2026-09-12.
- What it proves: the exact `id: <token>\ndata: <json>\n\n` wire shape
  (R-14.13's opaque resume-token id, the `{"seq","kind","source","payload"}`
  envelope `sseEventJSON` emits) for these six kinds, asserted against in
  `sse_filter_test.go` rather than a second, hand-typed dialect.
- Known gap this fixture does NOT close: no production caller in this
  tree publishes these six event kinds today (see `sse_jobs.go`'s doc
  comment) — the fixture proves the REGISTRATION/delivery mechanism this
  ticket owns, not a live job/lease event source, which is out of this
  ticket's boundaries ("No scheduler logic... added").
