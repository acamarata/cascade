package conversation

// Purpose: the transaction and sequencing helpers Store's appenders share,
//   split out of store.go to keep both files under the 300-line cap.
//   Behaviour-neutral move: the functions below are verbatim from store.go.
// SPORT: internal.conversation.store_tx/ADDED.

import (
	"context"
	"database/sql"
	"errors"

	mcsqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/acamarata/cascade/pkg/cascade"
)

// runTx commits fn's *sql.Tx on nil, else rolls back -- the
// existence/next-seq/insert sequence runs atomically, no TOCTOU window.
func runTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "conversation: begin transaction")
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "conversation: commit transaction")
	}
	return nil
}

// rowExists reports whether table has a row with id. Called before the
// out-of-order check so a same-id replay reports ErrImmutable, not
// ErrOutOfOrder; PRIMARY KEY is the structural backstop either way.
func rowExists(ctx context.Context, tx *sql.Tx, table, id string) (bool, error) {
	var exists int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM `+table+` WHERE id = ?`, id).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, cascade.Wrap(cascade.KindUnavailable, err, "conversation: check row exists")
	}
	return true, nil
}

// nextSeq returns the next expected 0-based Seq for table/idColumn/id: a
// proposed Seq that differs is refused as ErrOutOfOrder at the call site.
func nextSeq(ctx context.Context, tx *sql.Tx, table, idColumn, id string) (int64, error) {
	var count int64
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE `+idColumn+` = ?`, id).Scan(&count)
	if err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "conversation: read next seq")
	}
	return count, nil
}

// translateAppendError maps a raw INSERT error to ErrImmutable when it is
// a real SQLite constraint violation (via the driver's own result code,
// matching sqlhelpers.go's classifyProbeError -- never a string match),
// or wraps it as a generic storage failure otherwise.
func translateAppendError(err error, msg string) error {
	if err == nil {
		return nil
	}
	var sqliteErr *mcsqlite.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqlite3.SQLITE_CONSTRAINT {
		return ErrImmutable
	}
	return cascade.Wrap(cascade.KindUnavailable, err, msg)
}
