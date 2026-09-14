package conversation

// Purpose: thread archival state transitions (R-14.90, P9-Q archival spec
//   salvaged via ARCHIVE-MAP.md; P1-E20-W5-S44-T3).
// Inputs: a threadID and (for ArchiveThread) a caller-resolved Clock
//   reading -- this file reads no clock itself.
// Outputs: an archived/unarchived state recorded in a NEW marker table,
//   never a mutation of conversation_thread itself.
// Constraints: archiving never deletes or modifies turn/segment data --
//   presence of a conversation_thread_archive row IS the archived state;
//   its absence is "not archived". This is a new table rather than an
//   ALTER TABLE ... ADD COLUMN on conversation_thread because the migrate
//   DSL offers no ALTER step at all (see search.go's CONTRACT-VS-TREE
//   note for the identical FTS5 gap) -- dsl.go's own doc comment
//   prescribes exactly this shape: "adding a column ... means authoring a
//   new MigrationStep for a new table". text/num/id/fk/uniqueIdx below
//   moved here from domain.go (unchanged behavior, same package) to keep
//   domain.go under Art.10.3's 300-line cap once this ticket's two new
//   MigrationSet steps landed.
// SPORT: internal.conversation.archive/ADDED (P1-E20-W5-S44-T3).

import (
	"context"
	"database/sql"
	"errors"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// tableThreadArchive is the archived-state marker table: one row per
// archived thread id, absent for every non-archived thread.
const tableThreadArchive = "conversation_thread_archive"

// text/id/num/fk/uniqueIdx are terse column-literal builders so
// MigrationSet's (domain.go) table/index steps fit one line per column --
// pure formatting sugar, no behavior of their own.
func text(name string) migrate.ColumnDef {
	return migrate.ColumnDef{Name: name, Type: migrate.TypeText, NotNull: true}
}
func num(name string) migrate.ColumnDef {
	return migrate.ColumnDef{Name: name, Type: migrate.TypeInteger, NotNull: true}
}
func id(name string) migrate.ColumnDef {
	return migrate.ColumnDef{Name: name, Type: migrate.TypeText, PrimaryKey: true, NotNull: true}
}
func fk(col, refTable string) migrate.ForeignKeyDef {
	return migrate.ForeignKeyDef{Column: col, RefTable: refTable, RefColumn: "id"}
}
func uniqueIdx(name, table string, cols ...string) migrate.MigrationStep {
	return migrate.MigrationStep{Kind: migrate.StepCreateIndex, Description: "unique " + name,
		Index: &migrate.IndexDef{Name: name, Table: table, Columns: cols, Unique: true}}
}

// ArchiveThread implements Store. ErrThreadNotFound if threadID does not
// exist; idempotent otherwise (ON CONFLICT DO NOTHING -- re-archiving an
// already-archived thread keeps its original archivedAt).
func (s *conversationStore) ArchiveThread(ctx context.Context, threadID string, archivedAt int64) error {
	if threadID == "" {
		return ErrInvalidRecord
	}
	var exists string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM `+tableThread+` WHERE id = ?`, threadID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrThreadNotFound
	}
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "conversation: check thread exists")
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO `+tableThreadArchive+` (thread_id, archived_at) VALUES (?, ?) ON CONFLICT(thread_id) DO NOTHING`,
		threadID, archivedAt)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "conversation: archive thread")
	}
	return nil
}

// UnarchiveThread implements Store. A no-op, not an error, when threadID
// was not archived.
func (s *conversationStore) UnarchiveThread(ctx context.Context, threadID string) error {
	if threadID == "" {
		return ErrInvalidRecord
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM `+tableThreadArchive+` WHERE thread_id = ?`, threadID); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "conversation: unarchive thread")
	}
	return nil
}

// IsArchived implements Store.
func (s *conversationStore) IsArchived(ctx context.Context, threadID string) (bool, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT thread_id FROM `+tableThreadArchive+` WHERE thread_id = ?`, threadID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, cascade.Wrap(cascade.KindUnavailable, err, "conversation: check archived state")
	}
	return true, nil
}

// ArchiveThread exposes Store.ArchiveThread through the adapter surface
// (see pagination.go's doc comment: the future conversation acceptance
// flow, P1-E21-W5-S46-T5, not yet built, is R-14.90's own named consumer
// -- it excludes archived threads from context assembly by calling
// ListThreadsPage with the default, archived-excluding filter this and
// UnarchiveThread below gate).
func (a *Adapter) ArchiveThread(ctx context.Context, threadID string) error {
	return a.store.ArchiveThread(ctx, threadID, a.clock.Now().Unix())
}

// UnarchiveThread exposes Store.UnarchiveThread through the adapter surface.
func (a *Adapter) UnarchiveThread(ctx context.Context, threadID string) error {
	return a.store.UnarchiveThread(ctx, threadID)
}
