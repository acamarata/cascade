package conversation

// Purpose: Store, the append-only persisted CRUD surface over the three
//   tables domain.go's MigrationSet creates.
// Inputs: an open *sql.DB already migrated via ApplyConversationSchema.
// Outputs: typed pkg/cascade errors for malformed, missing, out-of-order,
//   or duplicate-id paths; never a bare *sql.Rows leak.
// Constraints: the ONLY exported mutators are AppendTurn and
//   AppendSegment -- no UpdateTurn/UpdateSegment exists, and no SQL UPDATE
//   ever targets conversation_turn/conversation_segment. Both INSERTs
//   below carry no ON CONFLICT clause (unlike internal/jobs.Store.PutJob's
//   deliberate upsert), so a duplicate primary key is refused by SQLite's
//   own constraint before this file's own check matters -- structural, not
//   advisory (see errors.go's ErrImmutable doc comment).
// SPORT: internal.conversation.store/ADDED (P1-E20-W5-S43-T1).

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	mcsqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Store is the append-only conversation persistence surface (ticket text
// calls this ConversationStore; named Store so the exported type does not
// stutter as conversation.ConversationStore -- revive/CI lint). A future
// in-memory or RPC-backed implementation (T2's adapter layer) can satisfy
// this without importing database/sql.
type Store interface {
	// AppendTurn appends turn to its thread (creating the thread row on
	// first use): ErrImmutable on a duplicate turn.ID, ErrOutOfOrder if
	// turn.Seq does not extend the sequence by one, else ErrInvalidRecord.
	AppendTurn(ctx context.Context, turn Turn) error
	// AppendSegment appends seg to its turn: ErrTurnNotFound if seg.TurnID
	// does not exist, ErrImmutable on a duplicate seg.ID, ErrOutOfOrder if
	// seg.Seq does not extend the sequence by one, else ErrInvalidRecord.
	AppendSegment(ctx context.Context, seg Segment) error
	// GetThread reads one thread by id; ok=false with a nil error means
	// "no such thread", never itself an error.
	GetThread(ctx context.Context, id string) (thread Thread, ok bool, err error)
	// ListTurns returns every turn in threadID, in insertion (Seq) order.
	ListTurns(ctx context.Context, threadID string) ([]Turn, error)
	// ListSegments returns every segment in turnID, in insertion order.
	ListSegments(ctx context.Context, turnID string) ([]Segment, error)
	// ListThreads returns every thread, ordered by CreatedAt then ID.
	ListThreads(ctx context.Context) ([]Thread, error)

	// ListTurnsPage returns a bounded, Seq-ordered page of threadID's
	// turns (pagination.go). Cursor=="" starts at the beginning; a
	// non-empty one must come from a prior call for this SAME threadID --
	// ErrBadCursor on a malformed or foreign token, never a panic.
	ListTurnsPage(ctx context.Context, threadID string, filter PaginationFilter) (TurnPage, error)
	// ListThreadsPage returns a bounded page of threads ordered like
	// ListThreads. Archived threads (archive.go) are excluded unless
	// filter.IncludeArchived is set.
	ListThreadsPage(ctx context.Context, filter PaginationFilter) (ThreadPage, error)
	// SearchTurns runs a parameterised FTS5 MATCH query over segment
	// content (search.go), ranked by SQLite's own bm25 relevance.
	// ErrSearchUnavailable with no fts5 index; adversarial MATCH syntax
	// is KindInvalidInput, never a panic, never interpolated SQL.
	SearchTurns(ctx context.Context, query string, filter SearchFilter) ([]TurnMatch, error)
	// ArchiveThread marks threadID archived as of archivedAt (a
	// caller-resolved Clock reading): excluded from ListThreadsPage's
	// default listing, every turn/segment unchanged (archive.go).
	ArchiveThread(ctx context.Context, threadID string, archivedAt int64) error
	// UnarchiveThread restores normal visibility; a no-op if not archived.
	UnarchiveThread(ctx context.Context, threadID string) error
	// IsArchived reports whether threadID carries an archive marker.
	IsArchived(ctx context.Context, threadID string) (bool, error)
	// PruneTurns logically tombstones turns meeting BOTH policy dimensions
	// (retention.go) as of now (a caller-resolved Clock reading).
	// Idempotent: a repeat call against unchanged data tombstones 0 rows.
	PruneTurns(ctx context.Context, policy RetentionPolicy, now int64) (PruneResult, error)
}

// conversationStore is the SQLite-backed Store impl. ftsOnce/ftsOK cache
// whether this db carries the search.go FTS5 index (probed lazily, once,
// via sqlite_master -- see hasFTS) so AppendSegment's per-append mirror
// write and SearchTurns's own refusal do not re-probe on every call.
type conversationStore struct {
	db      *sql.DB
	ftsOnce sync.Once
	ftsOK   bool
}

// NewStore wraps db, which must already carry the MigrationSet tables.
func NewStore(db *sql.DB) Store {
	return &conversationStore{db: db}
}

var _ Store = (*conversationStore)(nil)

// validateTurn/validateSegment check structural invariants pre-storage.
func validateTurn(turn Turn) error {
	if turn.ID == "" || turn.ThreadID == "" || turn.CreatedAt <= 0 || turn.Seq < 0 || !turn.Role.Valid() {
		return ErrInvalidRecord
	}
	return nil
}

func validateSegment(seg Segment) error {
	if seg.ID == "" || seg.TurnID == "" || seg.CreatedAt <= 0 || seg.Seq < 0 || !seg.Kind.Valid() {
		return ErrInvalidRecord
	}
	return nil
}

// AppendTurn implements Store. Contract gap (journal): no CreateThread
// exists, so a new thread's Name defaults to its own ThreadID below.
func (s *conversationStore) AppendTurn(ctx context.Context, turn Turn) error {
	if err := validateTurn(turn); err != nil {
		return err
	}
	return runTx(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO `+tableThread+` (id, name, created_at) VALUES (?, ?, ?)
			 ON CONFLICT(id) DO NOTHING`,
			turn.ThreadID, turn.ThreadID, turn.CreatedAt); err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "conversation: ensure thread")
		}

		exists, err := rowExists(ctx, tx, tableTurn, turn.ID)
		if err != nil {
			return err
		}
		if exists {
			return ErrImmutable
		}

		next, err := nextSeq(ctx, tx, tableTurn, "thread_id", turn.ThreadID)
		if err != nil {
			return err
		}
		if turn.Seq != next {
			return ErrOutOfOrder
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO `+tableTurn+` (id, thread_id, seq, role, created_at) VALUES (?, ?, ?, ?, ?)`,
			turn.ID, turn.ThreadID, turn.Seq, string(turn.Role), turn.CreatedAt)
		return translateAppendError(err, "conversation: append turn")
	})
}

// AppendSegment implements Store.
func (s *conversationStore) AppendSegment(ctx context.Context, seg Segment) error {
	if err := validateSegment(seg); err != nil {
		return err
	}
	// hasFTS is probed HERE, before the tx opens, never inside runTx's
	// closure: it queries s.db (the pool), and SetMaxOpenConns(1) (every
	// test here) has one connection to give out -- querying the pool
	// from inside an open *sql.Tx on it deadlocks waiting for the
	// connection that tx itself holds.
	fts := s.hasFTS(ctx)
	return runTx(ctx, s.db, func(tx *sql.Tx) error {
		var threadID string
		err := tx.QueryRowContext(ctx, `SELECT thread_id FROM `+tableTurn+` WHERE id = ?`, seg.TurnID).Scan(&threadID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTurnNotFound
		}
		if err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "conversation: check turn exists")
		}

		segExists, err := rowExists(ctx, tx, tableSegment, seg.ID)
		if err != nil {
			return err
		}
		if segExists {
			return ErrImmutable
		}

		next, err := nextSeq(ctx, tx, tableSegment, "turn_id", seg.TurnID)
		if err != nil {
			return err
		}
		if seg.Seq != next {
			return ErrOutOfOrder
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO `+tableSegment+` (id, turn_id, seq, kind, content, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			seg.ID, seg.TurnID, seg.Seq, string(seg.Kind), seg.Content, seg.CreatedAt)
		if err := translateAppendError(err, "conversation: append segment"); err != nil {
			return err
		}
		if fts {
			return mirrorSegmentFTS(ctx, tx, seg, threadID)
		}
		return nil
	})
}

// GetThread implements Store.
func (s *conversationStore) GetThread(ctx context.Context, id string) (Thread, bool, error) {
	var t Thread
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, created_at FROM `+tableThread+` WHERE id = ?`, id,
	).Scan(&t.ID, &t.Name, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Thread{}, false, nil
	}
	if err != nil {
		return Thread{}, false, cascade.Wrap(cascade.KindUnavailable, err, "conversation: get thread")
	}
	return t, true, nil
}

// ListTurns implements Store.
func (s *conversationStore) ListTurns(ctx context.Context, threadID string) ([]Turn, error) {
	return listRows(ctx, s.db,
		`SELECT id, thread_id, seq, role, created_at FROM `+tableTurn+` WHERE thread_id = ? ORDER BY seq ASC`,
		[]any{threadID}, scanTurn, "turns")
}

// ListSegments implements Store.
func (s *conversationStore) ListSegments(ctx context.Context, turnID string) ([]Segment, error) {
	return listRows(ctx, s.db,
		`SELECT id, turn_id, seq, kind, content, created_at FROM `+tableSegment+` WHERE turn_id = ? ORDER BY seq ASC`,
		[]any{turnID}, scanSegment, "segments")
}

// ListThreads implements Store.
func (s *conversationStore) ListThreads(ctx context.Context) ([]Thread, error) {
	return listRows(ctx, s.db,
		`SELECT id, name, created_at FROM `+tableThread+` ORDER BY created_at ASC, id ASC`,
		nil, scanThread, "threads")
}

// scanTurn/scanSegment/scanThread/listRows moved to pagination.go (shared
// by the plain and cursor-paginated listers alike) to keep this file
// under Art.10.3's 300-line cap once ListTurnsPage/ListThreadsPage's own
// interface additions landed above.

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
