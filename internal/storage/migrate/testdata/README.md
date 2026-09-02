# internal/storage/migrate/testdata

## golden/sqlite/, golden/postgres/

Byte-for-byte golden DDL fixtures for `TestGoldenSQLite`/`TestGoldenPostgres`
in `golden_test.go`, emitted from the same canonical reference
`MigrationSet` (`referenceMigrationSet()` in `golden_test.go`): a `users`
table (autoincrement integer primary key, TEXT/REAL/BLOB columns, a NOT
NULL + UNIQUE column), a `posts` table with a `FOREIGN KEY ... ON DELETE
CASCADE` back to `users`, and a `CREATE INDEX` on `posts(user_id)`.

- `golden/sqlite/0001_reference.sql` — `SQLiteEmitter{}.Emit(...)` output,
  captured by running this ticket's own emitter and reading it back (there
  is no independent SQLite DDL oracle to diff against the way Postgres has
  `psql`; the golden file's authority is that its statements are then
  EXECUTED against a real `modernc.org/sqlite` database in
  `TestIdempotentApply`/`TestGoldenSQLite`, proving they are not just
  self-consistent but actually valid, real SQLite DDL — Art.2's
  real-counterpart bar, applied to the SQLite half).
- `golden/postgres/0001_reference.sql` — `PostgresEmitter{}.Emit(...)`
  output.

## Postgres fixture provenance (Art.2 real-counterpart)

- **Tool:** `psql` (client) inside the official `postgres:16` Docker image,
  run via `docker run --rm postgres:16 ...` (no `psql`/Postgres server is
  installed on the build host itself).
- **Version:** PostgreSQL 16 (Debian 16.* image, `postgres:16` tag as
  published on Docker Hub at the time this ticket was built).
- **Date captured:** 2026-09-02 (original); re-captured 2026-09-02 under
  R-14.142 (see below — same day, second pass).
- **Method:** the exact statements in `golden/postgres/0001_reference.sql`
  were piped to `psql` running against a scratch `postgres:16` container
  and executed with `-v ON_ERROR_STOP=1`; a `\d+ users`, `\d+ posts` and a
  live `INSERT`/`SELECT` round-trip were run afterward to confirm the
  emitted DDL produces the intended schema (autoincrement identity column,
  FOREIGN KEY … ON DELETE CASCADE enforcement, index present) with a real
  Postgres server, not merely a syntax parse.
- **R-14.142 re-capture (BIGSERIAL, not SERIAL):** CR of this ticket found
  the original fixture used `SERIAL` (32-bit), silently inconsistent with
  the same emitter's `TypeInteger->BIGINT` (64-bit) mapping and with
  SQLite's 64-bit rowid-aliased AUTOINCREMENT — a high-volume table would
  run for years locally and then fail inserts in PRODUCTION ONLY once past
  2,147,483,647 rows. The fixture was re-captured against a fresh scratch
  `postgres:16` container (same image, same `docker run` method above):
  both `"id"` columns changed from `SERIAL PRIMARY KEY` to `BIGSERIAL
  PRIMARY KEY` (the only bytes that changed — column order, other types,
  the FOREIGN KEY clause, and the index are byte-identical to the original
  capture). Verified live: `information_schema.columns` reports `id` as
  `bigint` on both `users` and `posts`, and an `INSERT` of
  `9223372036854775807` (max int64, SQLite's own AUTOINCREMENT ceiling)
  round-trips successfully — confirming the two dialects now agree on
  range class, not merely on emitted spelling. See
  `ColumnDef.AutoIncrement`'s doc comment in `../dsl.go` for the full
  ruling and `autoincrement_range_test.go` for the automated proof.
- **Honesty note (this ticket's binding instruction):** this live-Postgres
  proof was performed manually via `docker run postgres:16` in the build
  session for this ticket (recorded in the ticket journal) — it is
  deliberately NOT part of the automated `go test ./internal/storage/
  migrate/...` gate, because this repo's CI environment has no Postgres
  server or Docker daemon available and a test that only passes when
  Docker happens to be present would be flaky-by-environment, not a real
  gate. The automated suite (`TestGoldenPostgres`) proves the emitter's
  output byte-for-byte against this file; it does NOT itself execute that
  SQL against a live Postgres. If this package's Postgres output ever
  needs re-verifying against a live server, repeat the `docker run
  postgres:16` procedure above and update this note's date.
