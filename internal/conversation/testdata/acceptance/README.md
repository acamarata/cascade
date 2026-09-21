# S-44.T5 acceptance fixture provenance

Ticket: P1-E20-W5-S44-T5 "Accept S-44: live-mirror and scrub invariants".
This directory exists to satisfy `files_scope.add` and to record, per
Art.2, the tool/version/date provenance of the real counterparts the five
`acceptance_*_test.go` files (`internal/conversation/`) drive against. It
introduces no new binary or golden fixture files of its own.

## SSE client (live-mirror, acceptance_livemirror_test.go)

- Tool: Go standard library `net/http.Client` with a custom
  `DialContext` dialing `net.Dialer.DialContext(ctx, "unix", socketPath)`
  — a real HTTP/1.1 client over a real unix-domain-socket listener, never
  a self-authored SSE parser or a fake transport.
- Version: Go toolchain `go1.26.6 darwin/arm64` (`go version`, captured
  2026-09-21, this ticket's build).
- Date captured: 2026-09-21.
- Server counterpart: `internal/daemon.Run` (the real daemon lifecycle
  entry point) serving `internal/rpc.NewSSEHandler` at `rpc.EventsPath`
  (`GET /events`) and the JSON-RPC registry at `rpc.RPCPath`
  (`POST /rpc`) — the same construction
  `sse_integration_test.go`'s `TestClientLocalEcho_RealSocket_EchoPrecedesResponse`
  (S-43.T2) uses; this ticket's harness (`newAcceptanceLiveMirrorHarness`)
  is that construction, extended to serve both directions of the
  live-mirror acceptance criterion from one running daemon instance.

## Scrub invariant sentinel (acceptance_scrub_test.go)

Reuses the existing, already-provenanced H/S-15.T3 golden corpus at
`internal/conversation/testdata/v1-goldens/scrub/single_span.golden`
(see that directory's own `testdata/README.md`) rather than introducing a
new fixture: the sentinel `sk-Canary0000AAAA1111BBBB2222CCCC3333` and its
expected tag `<apikey>OPENAI_API_KEY</apikey>` are read from that file at
test time via the package's own `loadScrubGolden` helper
(`scrub_golden_test.go`), not retyped as a literal.

## Providers registry (privacy modes, acceptance_privacy_test.go)

The two-lane and external-only provider registries are built with the
real `internal/providers/registry` package (`ApplyMigrationSchema`,
`AddProvider`, `UpsertLane`) over a real, file-backed `modernc.org/sqlite`
database under `t.TempDir()` — no fixture file; the two provider/lane
records are constructed in Go at test time
(`acceptance_harness_test.go`).

## Journal resume (acceptance_journal_test.go)

Uses the real `internal/fleet/journal.SQLiteStore` construction
(`journal.New`) journal_test.go's own `newTestJournal` helper already
establishes for S-44.T4 — no new fixture.
