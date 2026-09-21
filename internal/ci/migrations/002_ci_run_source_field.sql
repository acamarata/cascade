-- ci_run_source
-- schema_version: 9  minimum_reader_version: 9
--
-- REFERENCE ONLY. This file is a human-readable rendering of the schema
-- internal/ci/domain_source.go's sourceTableStep actually creates. It is
-- never imported, parsed, or executed by any Go code path in this repo --
-- the same convention 001_ci_results.sql and every other domain's own
-- "reference only" migration file already establishes.
--
-- Every executable migration in this tree is authored through the
-- portable Go DSL at internal/storage/migrate (TableDef/ColumnDef/
-- IndexDef/ForeignKeyDef). This is an ADDITIVE migration: a NEW table,
-- never an ALTER TABLE ... ADD COLUMN on ci_run itself -- the DSL is
-- CREATE-only (dsl.go's own doc comment), so a column added to an
-- existing table's shape is always expressed as a new table, matching
-- internal/conversation/archive.go's conversation_thread_archive
-- precedent for the identical gap.
--
-- ci_run_source records which producer wrote a given ci_run row:
-- 'github-actions' (P1-E25-W5-S51-T2's polling client, via
-- internal/ci.Upsert) or 'local' (this ticket's local CI runner, via
-- internal/ci.UpsertRunSource). A ci_run row with NO corresponding
-- ci_run_source row is read back as 'github-actions' by
-- internal/ci.runSource -- every row T2's own Upsert ever wrote predates
-- this table, so an absent entry can only mean that producer. This is
-- exactly what makes the change backward-compatible: T2's own tests and
-- write path are untouched by this migration.
--
-- NO FOREIGN KEY. ci_run's primary key is the COMPOSITE (run_id,
-- repo_id), and internal/storage/migrate's ForeignKeyDef is single-column
-- by construction, so the constraint this file once rendered (run_id ->
-- ci_run.run_id) pointed at a non-unique column and is rejected outright
-- the moment PRAGMA foreign_keys is ON. ci_run_source is a MARKER table
-- instead, on the same terms as conversation_thread_privacy: a row
-- describes a run, an absent row is DEFINED as 'github-actions', and a
-- row whose run is gone is harmless rather than an integrity error.
--
-- The statements below match the SQLite-dialect rendering domain.go's
-- MigrationSet (as extended by domain_source.go's sourceTableStep)
-- produces via migrate.SQLiteEmitter{}.Emit -- asserted present in the
-- real emitted DDL by domain_source_test.go's
-- TestMigrationSetReferenceShape_SourceTable.

CREATE TABLE IF NOT EXISTS "ci_run_source" (
    "run_id" INTEGER NOT NULL,
    "repo_id" INTEGER NOT NULL,
    "source" TEXT NOT NULL,
    PRIMARY KEY ("run_id", "repo_id")
);
