# T3 golden fixture provenance — export-seed.jsonl

Ticket P1-E02-W1-S03-T3 (Per-Domain JSON Export/Import) documents its golden
fixture's capture provenance here rather than appending to
`internal/storage/testdata/README.md`: that file is owned in this same
batch by a concurrent sibling ticket (B/S-03.T2, retention), and editing it
here would race that ticket's own in-flight edits. This file is T3's
equivalent of the README's per-ticket provenance entries (see that file's
existing T1/T2 sections for the established format T3 follows) and should
be folded into `README.md` as a normal section the next time that file is
next touched by a ticket that owns it outright.

## `internal/storage/testdata/golden/export-seed.jsonl`

- **Tool:** `modernc.org/sqlite` (pure-Go, no CGO — the same driver
  `providers/sqlite` and every other `internal/storage` test file ships
  against).
- **Version:** `v1.58.0`, read from this module's `go.mod` `require` block
  at the time this ticket was built (2026-09-02).
- **Date:** 2026-09-02.
- **Command/test that produced it:** `internal/storage/export_test.go`'s
  `TestExportGolden`, run once with `CASCADE_TESTKIT_UPDATE_GOLDEN=1` set
  locally (never in CI — `internal/testkit.Golden`'s own CI guard is
  mirrored by this ticket's `compareOrUpdateExportGolden` helper in
  `export_helpers_test.go`):

  ```sh
  CASCADE_TESTKIT_UPDATE_GOLDEN=1 go test ./internal/storage/... -run TestExportGolden -v
  ```

  Every subsequent CI/local run invokes the same test WITHOUT that env var,
  which instead reads and byte-compares against the committed file.
- **Domain state captured:** a real modernc-sqlite database, freshly
  `storage.Bootstrap`-ed (stamps `schema_version = 1`) with
  `storage.SetExportClock` frozen at `2026-09-01T12:00:00Z`
  (`export_test.go`'s `exportTestClock`), then three rows inserted directly
  into the shared `kv` table under the `context` domain's namespace
  (`export_test.go`'s `canonicalGoldenRows`):
  - `alpha` -> `"hello world"` (a plain ASCII value)
  - `beta` -> `""` (an empty value — NULL is not representable, since the
    `kv` table's `value` column is `BLOB NOT NULL`, so the empty string is
    this format's edge case for "no content")
  - `gamma` -> `{0x00, 0x01, 0xFE, 0xFF}` (non-UTF8 binary bytes, proving
    the base64 wire encoding round-trips arbitrary bytes rather than
    silently mangling them)
- **What is and is not exercised:** the golden proves the header shape,
  field order, and base64 row encoding are stable and reproducible under a
  frozen clock; it deliberately does NOT cover the "awkward" round-trip
  cases (very large values, quoted/newline/unicode keys) — those are
  exercised by `TestImportRoundTrip` (`export_roundtrip_test.go`) via
  live Export/Import calls rather than a second golden file, since their
  whole point is to prove round-trip fidelity of arbitrary data, not a
  fixed wire-format snapshot.
