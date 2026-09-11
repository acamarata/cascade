-- conversation_thread / conversation_turn / conversation_segment
-- schema_version: 7  minimum_reader_version: 7
--
-- REFERENCE ONLY. This file is a human-readable rendering of the schema
-- internal/conversation/domain.go's MigrationSet actually creates. It is
-- never imported, parsed, or executed by any Go code path in this repo.
--
-- Every executable migration in this tree is authored through the
-- portable Go DSL at internal/storage/migrate (TableDef/ColumnDef/
-- IndexDef), which compiles to dialect-correct DDL for both SQLite and
-- Postgres and validates every identifier before it reaches emitted SQL
-- text. No package anywhere in this tree hand-authors or executes a raw
-- .sql migration file; the only other *.sql files in the repo are golden
-- test fixtures EMITTED BY that same DSL
-- (internal/storage/migrate/testdata/golden/*), never the reverse. This
-- file exists because the ticket's files_scope names it explicitly; see
-- domain.go's package doc comment ("RAW-SQL MIGRATION FILE") and this
-- ticket's journal for the full contract-vs-tree contradiction, quoted
-- both ways.
--
-- The statements below match the SQLite-dialect rendering domain.go's
-- MigrationSet produces via migrate.SQLiteEmitter{}.Emit -- every table,
-- column, foreign key, and unique index named here is asserted present
-- in the real emitted DDL by domain_test.go's
-- TestMigrationSetReferenceShape.

CREATE TABLE IF NOT EXISTS "conversation_thread" (
    "id" TEXT NOT NULL,
    "name" TEXT NOT NULL,
    "created_at" INTEGER NOT NULL,
    PRIMARY KEY ("id")
);

CREATE TABLE IF NOT EXISTS "conversation_turn" (
    "id" TEXT NOT NULL,
    "thread_id" TEXT NOT NULL,
    "seq" INTEGER NOT NULL,
    "role" TEXT NOT NULL,
    "created_at" INTEGER NOT NULL,
    PRIMARY KEY ("id"),
    FOREIGN KEY ("thread_id") REFERENCES "conversation_thread"("id")
);

CREATE UNIQUE INDEX IF NOT EXISTS "idx_conversation_turn_thread_seq"
    ON "conversation_turn" ("thread_id", "seq");

CREATE TABLE IF NOT EXISTS "conversation_segment" (
    "id" TEXT NOT NULL,
    "turn_id" TEXT NOT NULL,
    "seq" INTEGER NOT NULL,
    "kind" TEXT NOT NULL,
    "content" TEXT NOT NULL,
    "created_at" INTEGER NOT NULL,
    PRIMARY KEY ("id"),
    FOREIGN KEY ("turn_id") REFERENCES "conversation_turn"("id")
);

CREATE UNIQUE INDEX IF NOT EXISTS "idx_conversation_segment_turn_seq"
    ON "conversation_segment" ("turn_id", "seq");
