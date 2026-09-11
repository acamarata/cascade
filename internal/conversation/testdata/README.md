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
