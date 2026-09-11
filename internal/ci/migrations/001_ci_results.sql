-- ci_run / ci_job / ci_step
-- schema_version: 8  minimum_reader_version: 8
--
-- REFERENCE ONLY. This file is a human-readable rendering of the schema
-- internal/ci/domain.go's MigrationSet actually creates. It is never
-- imported, parsed, or executed by any Go code path in this repo -- the
-- same convention internal/conversation/migrations/001_conversation_tables.sql
-- and internal/providers/registry's own precedent already establish.
--
-- Every executable migration in this tree is authored through the
-- portable Go DSL at internal/storage/migrate (TableDef/ColumnDef/
-- IndexDef/ForeignKeyDef), which compiles to dialect-correct DDL for both
-- SQLite and Postgres and validates every identifier before it reaches
-- emitted SQL text.
--
-- The statements below match the SQLite-dialect rendering domain.go's
-- MigrationSet produces via migrate.SQLiteEmitter{}.Emit -- every table,
-- column and foreign key named here is asserted present in the real
-- emitted DDL by domain_test.go's TestMigrationSetReferenceShape.

CREATE TABLE IF NOT EXISTS "ci_run" (
    "run_id" INTEGER NOT NULL,
    "repo_id" INTEGER NOT NULL,
    "name" TEXT NOT NULL,
    "head_branch" TEXT NOT NULL,
    "head_sha" TEXT NOT NULL,
    "status" TEXT NOT NULL,
    "conclusion" TEXT NOT NULL,
    "created_at" INTEGER NOT NULL,
    "updated_at" INTEGER NOT NULL,
    PRIMARY KEY ("run_id", "repo_id")
);

CREATE TABLE IF NOT EXISTS "ci_job" (
    "job_id" INTEGER NOT NULL,
    "run_id" INTEGER NOT NULL,
    "name" TEXT NOT NULL,
    "status" TEXT NOT NULL,
    "conclusion" TEXT NOT NULL,
    "started_at" INTEGER,
    "finished_at" INTEGER,
    PRIMARY KEY ("job_id")
);

CREATE TABLE IF NOT EXISTS "ci_step" (
    "job_id" INTEGER NOT NULL,
    "number" INTEGER NOT NULL,
    "name" TEXT NOT NULL,
    "status" TEXT NOT NULL,
    "conclusion" TEXT NOT NULL,
    "started_at" INTEGER,
    "finished_at" INTEGER,
    PRIMARY KEY ("job_id", "number"),
    FOREIGN KEY ("job_id") REFERENCES "ci_job"("job_id") ON DELETE CASCADE
);
