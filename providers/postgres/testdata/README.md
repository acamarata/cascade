# providers/postgres/testdata — storetest-under-docker lane provenance

This directory has no fixtures. It exists to record the provenance and the
honest scope of the `storetest-under-docker` CI job
(`.github/workflows/ci.yml`), per 12-QUALITY-CONSTITUTION.md Art.2
(real-counterpart verification claims must be accurate, never implied
beyond what actually ran).

## What the lane runs (P1-E17-W4-S38-T4, current state)

- **Image:** `pgvector/pgvector:pg16` — a Postgres 16 image with the
  pgvector extension available (this job's service container is shared
  with the `pgvector-storetest-under-docker` job, which needs the
  extension; a plain `postgres:17` image, used before this ticket, does
  not carry it).
- **Command:** `go test -tags="postgres integration" -count=1 -race
  ./providers/postgres/... -v`.
- **Job status:** REQUIRED (`continue-on-error` removed by this ticket).

## History

**P1-E02-W1-S03-T5 (2026-09-02):** `providers/postgres/store.go` was a
total build-tagged stub — every `provider.Store` method returned
`cascade.ErrUnsupported` without touching the wire. Every
`storetest.RunStoreTests` sub-test failed by construction, so the job
carried `continue-on-error: true` (allowed-fail per 06-FORGE-SPEC.md
§5.19, D-2: "the LATER ticket's AC carries the integration test").

**P1-E17-W4-S38-T4 (this ticket) CLOSES that leg.** `store.go` is deleted;
the real driver (`postgres.go` + `postgres_store.go` + `postgres_tx.go` +
`postgres_errors.go` + `postgres_migrate.go`) speaks the actual Postgres
wire via `jackc/pgx/v5`'s `stdlib` `database/sql` adapter (pure Go, no
CGO). `storetest.RunStoreTests` passes for real against the live
`pgvector/pgvector:pg16` service container — see
`providers/postgres/integration_test.go`'s `TestPostgresStoretestUnderDocker`.
The same file's `TestPostgresLiveMigration` proves
`internal/storage/migrate`'s Postgres dialect applies live: ordered apply,
`schema_version`, `MinimumReaderVersion` downgrade refusal, and idempotent
re-apply.

**What this lane proves:** real Postgres wire behavior for every
`provider.Store` method including transactions and `CompareAndSwap`
conflict detection, `-race` clean; live schema migration; the named error
paths (unreachable server, missing env-ref — `internal/runtime`'s own
lane — auth failure). See `docs/storage.md` §Server profile for the full
picture, including the collation gotcha this ticket found live
(`postgres.go`'s `schemaDDL` doc comment).

**What this lane does NOT prove:** the Redis/S3 legs of the server
profile (S-38.T6/T7, not yet built) or the cross-machine sync round-trip
(S-38.T5, gate_only).
