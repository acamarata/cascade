# providers/pgvector/testdata — pgvector-storetest-under-docker lane provenance

This directory has no fixtures. It exists to record the provenance and the
honest scope of the `pgvector-storetest-under-docker` CI job
(`.github/workflows/ci.yml`), per 12-QUALITY-CONSTITUTION.md Art.2.

## What the lane runs (P1-E17-W4-S38-T4)

- **Image:** `pgvector/pgvector:pg16` — shared with the
  `storetest-under-docker` job (the same server backs both the Store and
  VectorStore family conformance suites).
- **Command:** `go test -tags="postgres integration" -count=1 -race
  ./providers/pgvector/... -v`.
- **Job status:** REQUIRED from the first commit — this driver has no
  prior stub/allowed-fail history; it did not exist before this ticket.

## What this lane proves

`internal/storage/storetest.RunVectorStoreTests` passes for real against
the live pgvector-enabled server: `Upsert`/`Query`/`Delete`, `Count`,
`Namespaces`, the `TopK` cap, and idempotent delete-of-absent. It also
proves the "pgvector extension missing" refusal
(`TestPgvectorOpen_ExtensionMissing`, skipped unless
`CASCADE_TEST_POSTGRES_NO_VECTOR_DSN` names a real server without the
extension — never simulated) and that mixing embedding dimensionalities
within one namespace surfaces as a typed `cascade.KindInvalidInput`
(`TestQuery_DimensionMismatchIsInvalidInput`), not a generic backend
error.

## What this lane does NOT prove

Nothing else in the server profile (Redis/S3, S-38.T6/T7) — those slots
are genuinely absent, never stubbed. See `docs/storage.md` §Server
profile for the full picture.
