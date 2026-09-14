package conversation

// Purpose: SQLite FTS5 full-text search over segment content
//   (P1-E20-W5-S44-T3), populated on append (no background re-index step
//   in P1).
// Inputs: an open, migrated *sql.DB whose dialect MAY or may not be
//   SQLite -- ensureFTS5 only creates the virtual table on the SQLite
//   dialect (migrate.SQLiteEmitter), since FTS5 has no portable Postgres
//   equivalent and the migrate DSL (internal/storage/migrate/dsl.go) has
//   no virtual-table or ALTER TABLE step kind at all (StepKind is closed
//   at StepCreateTable/StepCreateIndex -- see dsl.go's own doc comment:
//   "there is no ALTER TABLE step ... no raw SQL type escape hatch").
//   CONTRACT-VS-TREE (journal, quoted in full there): this ticket's text
//   says the FTS5 table is created "via the domain migration builder";
//   the migration builder cannot express a virtual table, so ensureFTS5
//   issues one raw, hand-validated DDL statement directly against db
//   instead -- no caller input reaches it, so no injection surface.
// Outputs: TurnMatch records ranked by SQLite's own bm25() relevance.
// Constraints: search.go never interpolates a caller's query string into
//   SQL text -- MATCH is always bound as a parameter (database/sql
//   placeholder), so adversarial FTS5 query syntax can produce a query
//   PARSE error (refused as KindInvalidInput) but never SQL injection.
// SPORT: internal.conversation.search/ADDED (P1-E20-W5-S44-T3).

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// tableTurnFTS is the FTS5 virtual table search.go mirrors every appended
// Segment's content into (store.go's AppendSegment, via mirrorSegmentFTS).
const tableTurnFTS = "conversation_turn_fts"

// ensureFTS5 idempotently creates the FTS5 index when dialect is SQLite;
// a nil dialect or any other dialect name is a documented no-op (search
// simply stays unavailable on that profile -- see ErrSearchUnavailable).
func ensureFTS5(ctx context.Context, db *sql.DB, dialect migrate.Dialect) error {
	if dialect == nil || dialect.Name() != "sqlite" {
		return nil
	}
	_, err := db.ExecContext(ctx,
		`CREATE VIRTUAL TABLE IF NOT EXISTS `+tableTurnFTS+` USING fts5(content, turn_id UNINDEXED, thread_id UNINDEXED)`)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "conversation: create fts5 index")
	}
	return nil
}

// hasFTS reports whether s.db carries the FTS5 index, probed once (via
// sqlite_master, portable across a driver that lacks it entirely --
// Postgres has no sqlite_master, so the probe itself just fails closed to
// false) and cached on the store for every subsequent call.
func (s *conversationStore) hasFTS(ctx context.Context) bool {
	s.ftsOnce.Do(func() {
		var name string
		err := s.db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, tableTurnFTS,
		).Scan(&name)
		s.ftsOK = err == nil
	})
	return s.ftsOK
}

// mirrorSegmentFTS inserts seg's content into the FTS5 index within the
// same transaction as its AppendSegment row -- "populated on append", per
// this ticket's own contract, never a separate re-index pass.
func mirrorSegmentFTS(ctx context.Context, tx *sql.Tx, seg Segment, threadID string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO `+tableTurnFTS+` (content, turn_id, thread_id) VALUES (?, ?, ?)`,
		seg.Content, seg.TurnID, threadID)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "conversation: index segment for search")
	}
	return nil
}

// SearchFilter narrows a SearchTurns call. ThreadID == "" searches every
// thread. Limit <=0 uses defaultPageSize; above maxPageSize is clamped,
// matching PaginationFilter's own bound (pagination.go).
type SearchFilter struct {
	ThreadID string
	Limit    int
}

// TurnMatch is one SearchTurns hit: the full matching Turn plus its
// bm25() rank (lower is more relevant, SQLite's own convention).
type TurnMatch struct {
	Turn Turn
	Rank float64
}

// SearchTurns implements Store.
func (s *conversationStore) SearchTurns(ctx context.Context, query string, filter SearchFilter) ([]TurnMatch, error) {
	if !s.hasFTS(ctx) {
		return nil, ErrSearchUnavailable
	}
	hits, err := s.searchHits(ctx, query, filter)
	if err != nil {
		return nil, err
	}
	out := make([]TurnMatch, 0, len(hits))
	for _, h := range hits {
		turn, err := s.getTurnByID(ctx, h.turnID)
		if err != nil {
			return nil, err
		}
		out = append(out, TurnMatch{Turn: turn, Rank: h.rank})
	}
	return out, nil
}

type searchHit struct {
	turnID string
	rank   float64
}

// searchHits runs the parameterised FTS5 MATCH query and returns the
// ranked (turn_id, rank) pairs, deduped so a turn whose several segments
// all match content is reported once, at its best rank. Dedup happens in
// Go, not SQL: SQLite refuses bm25() wrapped in an aggregate/GROUP BY
// ("unable to use function bm25 in the requested context"), so this
// selects every matching row in ascending rank order (SQLite's own
// convention: lower is more relevant) and keeps only the FIRST -- and
// therefore best -- occurrence of each turn_id.
func (s *conversationStore) searchHits(ctx context.Context, query string, filter SearchFilter) ([]searchHit, error) {
	sqlQuery := `SELECT turn_id, bm25(` + tableTurnFTS + `) AS rank FROM ` + tableTurnFTS +
		` WHERE ` + tableTurnFTS + ` MATCH ? `
	args := []any{query}
	if filter.ThreadID != "" {
		sqlQuery += `AND thread_id = ? `
		args = append(args, filter.ThreadID)
	}
	sqlQuery += `ORDER BY rank ASC`

	rows, err := s.db.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, translateSearchError(err)
	}
	defer func() { _ = rows.Close() }()

	seen := make(map[string]bool)
	var hits []searchHit
	for rows.Next() {
		var h searchHit
		if err := rows.Scan(&h.turnID, &h.rank); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "conversation: scan search hit")
		}
		if seen[h.turnID] {
			continue
		}
		seen[h.turnID] = true
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "conversation: iterate search hits")
	}
	if limit := clampLimit(filter.Limit); len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// getTurnByID reads one turn by its content-addressed id.
func (s *conversationStore) getTurnByID(ctx context.Context, id string) (Turn, error) {
	var t Turn
	var role string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, thread_id, seq, role, created_at FROM `+tableTurn+` WHERE id = ?`, id,
	).Scan(&t.ID, &t.ThreadID, &t.Seq, &role, &t.CreatedAt)
	if err != nil {
		return Turn{}, cascade.Wrap(cascade.KindUnavailable, err, "conversation: get turn by id")
	}
	r, err := DecodeRole(role)
	t.Role = r
	return t, err
}

// translateSearchError refuses any query-execution failure at the MATCH
// call site as KindInvalidInput: the schema is already known valid at
// this point (hasFTS already confirmed the table exists), so the only
// realistic failure here is a malformed FTS5 query expression -- modernc
// surfaces that as a plain driver error with no dedicated result code
// (unlike store.go's translateAppendError, which matches SQLite's own
// CONSTRAINT code), so this wraps on message alone rather than a code.
func translateSearchError(err error) error {
	return cascade.Wrap(cascade.KindInvalidInput, err, "conversation: search query rejected")
}

// SearchTurns exposes Store.SearchTurns through the adapter surface (see
// pagination.go's ListTurnsPage/ListThreadsPage doc comment: the same
// not-yet-built P1-E20-W5-S43-T4 MCP tool, cascade_cpa_search, is this
// operation's expected future caller -- its own contract text names this
// ticket's FTS5 backend as its intended swap-in replacement).
func (a *Adapter) SearchTurns(ctx context.Context, query string, filter SearchFilter) ([]TurnMatch, error) {
	return a.store.SearchTurns(ctx, query, filter)
}
