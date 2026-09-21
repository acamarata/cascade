# internal/conversation/testdata: provenance

## Art.2 real-counterpart provenance

- **Driver:** `modernc.org/sqlite` (pure-Go SQLite), version `v1.58.0` as
  pinned in the repository root `go.mod`.
- **Method:** every migration and store test in this package opens a real
  database FILE under `t.TempDir()` via `database/sql.Open("sqlite",
  <path>)` (`domain_test.go`'s `openTestDB`) -- never an in-memory double
  and never a self-authored schema stand-in. `ApplyConversationSchema`
  (domain.go) runs the real `internal/storage/migrate` DSL against that
  file, and assertions read the result back through `sqlite_master`
  (table presence) and `PRAGMA table_info(...)` (column presence)
  directly against the live driver -- never a parsed copy of domain.go's
  own `TableDef` values.
- **Round-trip coverage:** every store test (`store_test.go`,
  `conversation_test.go`) writes through `Store` and reads back through
  the same real connection, so the DSL's emitted SQLite DDL (PRIMARY KEY
  refusal on a duplicate append, the composite UNIQUE index over
  `(thread_id, seq)` / `(turn_id, seq)`, the FOREIGN KEY to a
  nonexistent turn) is exercised for real against the live driver's own
  result codes, never mocked or string-matched.

## No real transcripts

Every fixture value in this package's tests (turn/segment content,
thread names, role strings) is a short synthetic literal invented for
the test ("hello", "reply one", "seg-a") -- never a real chat transcript,
a real name, a real email address, a `/Volumes` or `/Users` path, a
machine or account identifier, or a credential-shaped string. This is a
PUBLIC repository; see conversation_test.go's `TestErrorsNeverEchoContent`
for the enforced proof that a synthetic marker placed in Content never
reaches an error's message text.

## SSE fixture provenance (P1-E20-W5-S43-T2)

- **Capture tool:** the repository's own real counterparts, not a
  recorded/replayed capture file. `sse_integration_test.go`'s
  `TestClientLocalEcho_RealSocket_EchoPrecedesResponse` drives the SSE
  wire format LIVE, end to end: a real `net/http` client subscribes to
  `GET /events` over a real unix-socket listener started by
  `internal/daemon.Run` (the same entry point the production daemon
  uses), a real `chat.append_turn` JSON-RPC call is POSTed over the same
  socket, and the test parses the resulting `event:`/`data:`/`id:`/
  `retry:` framing `internal/rpc/sse.go`'s `writeSSEEvent` emits (never a
  second, self-authored parser) to prove the CLIENT-LOCAL ECHO event
  arrives before the JSON-RPC response is read.
- **Version:** `internal/rpc` at the commit this ticket lands in
  (`writeSSEEvent`'s frame shape is unchanged since D/S-06.T4); Go
  `net/http` from the repository's pinned toolchain (`go.mod`).
- **Date:** 2026-09-11.
- **Why no static `.txt`/`.golden` fixture file:** the SSE stream's
  content includes a monotonically assigned `id:` (the bus's own Seq)
  and a wall-clock-adjacent `Timestamp` field inside the substituted JSON
  payload, so a byte-frozen static fixture would either need to hide
  those fields (weakening the proof this is real streamed output) or go
  stale every time the bus's Seq allocation order shifts. The live
  integration test is the fixture: it is Art.2's "real counterpart",
  captured fresh on every CI run rather than checked in once and drifting
  from the code that produces it.

## Scrub pipeline fixture provenance (P1-E20-W5-S44-T1)

`v1-goldens/scrub/single_span.golden` and `multi_span.golden` are byte-for-byte
copies of `input`/`canaries`/`expected_output` from (`expected_tag` is not
in the corpus; it is derived from the corpus's `expected_output` and
`hits[].name` and written here so the golden test can assert it)
`internal/testdata/secrets/goldens/single_span.yaml`
and `multi_span.yaml` -- the H/S-15.T3 secret-detector's own golden corpus,
already proven against the real `secrets.Detector`/`secrets.Rewriter` by
`internal/secrets/goldenfixture_test.go`. Copied with a Python byte-comparison
(`input` field identical, verified, not retyped by hand) rather than re-authored,
per Art.2 and ARCHIVE-MAP.md's "copy bytes, never source" rule.

`expected_output` is copied, not authored: it is the corpus's own recorded
rewrite of that input, so `scrub_test.go` asserts the pipeline's result
against the fixture rather than against a literal a test retyped.
`scrub_golden_test.go`'s loader reads it, and `assertGoldenOutput` is the
only place an expected value comes from.

Two fixtures are DERIVED IN THE TEST from the corpus canary rather than
added as files, and each says so at its use site:
`TestScrubVault_TwoHitsOneSuggestedName` (scrub_vault_test.go) builds a
second value by changing the canary's body counter, because no corpus
fixture carries two hits the detector names identically; and
`testScrubStraddlesSegments` (scrub_refusal_test.go) splits
`single_span.golden`'s own input inside its canary, because no corpus
fixture is a multi-segment turn. Neither invents a credential shape: both
are the corpus's own bytes, cut or re-numbered.

`cmd/cascade/chat_wiring_custody_test.go` reads
`internal/testdata/secrets/goldens/single_span.yaml` directly for the same
reason -- that package has no access to this one's testdata, and restating
a credential-shaped literal in a public repo is what the corpus exists to
avoid.

The directory name says `v1-goldens` because that is what this ticket's
files_scope names; the actual source is this repository's OWN live H/S-15.T3
corpus (captured 2026-09-05 per that corpus's own README), not the archived v1
tree -- a live, currently-tested corpus is Art.2's stronger "real counterpart"
than a frozen v1 snapshot would be, and reusing it means scrub.go's fixtures can
never drift from the detector/rewriter that actually processes them.

`testScrubRewriteRefusal` (scrub_refusal_test.go) additionally harvests the exact
literal `"wifi password: 7Kq2mZx9PLw4Rt6VbN3sQe8Hj1Cd5Fg0"` from
`internal/secrets/detector_test.go`'s `TestNamedEntropyIsCorroborated` -- a real,
already-proven case of a corroborated high-entropy span with no `TagFor`
mapping, which is what makes the real `secrets.Rewriter` refuse it.
