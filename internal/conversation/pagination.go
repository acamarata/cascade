package conversation

// Purpose: cursor-based pagination over conversation_turn and
//   conversation_thread listings (P1-E20-W5-S44-T3), plus the scanTurn/
//   scanSegment/scanThread/listRows plumbing store.go's plain listers
//   share with the paginated ones below -- moved here (unchanged
//   behavior) so store.go stays under Art.10.3's 300-line cap once this
//   ticket's interface additions landed.
// Inputs: a PaginationFilter whose Cursor is either "" (first page) or an
//   opaque token this package itself issued from a prior call.
// Outputs: a TurnPage/ThreadPage; NextCursor == "" means "no further
//   page". A malformed or foreign cursor is ErrBadCursor, never a panic.
// Constraints: the cursor format (base64url over a small tagged JSON
//   payload) is deliberately opaque -- callers must round-trip the
//   Cursor value verbatim and never construct or parse one themselves.
//   06-FORGE-SPEC §5.7 requires a fuzz target over the decoder
//   (cursor_fuzz_test.go); no CGO, no bare time.Now (this file reads no
//   clock at all -- pagination has no time dimension).
// SPORT: internal.conversation.pagination/ADDED (P1-E20-W5-S44-T3).

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Cursor is an opaque, server-issued pagination token. The zero value ""
// means "start from the beginning" everywhere a Cursor is accepted.
type Cursor string

// PaginationFilter bounds and positions one page of a ListTurnsPage or
// ListThreadsPage call. Limit <=0 uses defaultPageSize; a Limit above
// maxPageSize is clamped, never refused -- no caller-supplied unbounded
// read is possible through this filter.
type PaginationFilter struct {
	Cursor          Cursor
	Limit           int
	IncludeArchived bool // ListThreadsPage only; ignored by ListTurnsPage
}

// TurnPage is one bounded, Seq-ordered page of a thread's turns.
type TurnPage struct {
	Turns      []Turn
	NextCursor Cursor // "" on the last page
}

// ThreadPage is one bounded page of threads, ordered like ListThreads.
type ThreadPage struct {
	Threads    []Thread
	NextCursor Cursor // "" on the last page
}

// defaultPageSize/maxPageSize are this domain's per-call page-size bound
// (ticket text: "bounded by a per-domain constant").
const (
	defaultPageSize = 20
	maxPageSize     = 100
)

func clampLimit(n int) int {
	if n <= 0 {
		return defaultPageSize
	}
	if n > maxPageSize {
		return maxPageSize
	}
	return n
}

// The two opaque cursor kinds this package issues. cursorKindTurn cursors
// also carry the thread id they were issued for, so a cursor from thread
// A used against thread B decodes cleanly but is refused as ErrBadCursor
// at the call site (see ListTurnsPage) rather than silently resuming at
// the wrong offset.
const (
	cursorKindTurn   = "t"
	cursorKindThread = "h"
)

type turnCursorPayload struct {
	ThreadID string `json:"t"`
	Seq      int64  `json:"s"`
}

type threadCursorPayload struct {
	CreatedAt int64  `json:"c"`
	ID        string `json:"i"`
}

// encodeCursor renders v (one of the two payload types above) as an
// opaque Cursor: "<kind>." + base64url(json(v)).
func encodeCursor(kind string, v any) (Cursor, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err, "conversation: encode cursor")
	}
	return Cursor(kind + "." + base64.RawURLEncoding.EncodeToString(b)), nil
}

// decodeCursor is the sole entry point external bytes reach: c may be
// anything a caller hands back, including bytes this package never
// issued. wantKind gates the token's own kind prefix; v is decoded with
// DisallowUnknownFields so a payload shaped for the OTHER cursor kind (or
// garbage) is refused rather than silently zero-filled. Every failure
// returns ErrBadCursor, never a panic -- see cursor_fuzz_test.go.
func decodeCursor(c Cursor, wantKind string, v any) error {
	prefix := wantKind + "."
	s := string(c)
	if !strings.HasPrefix(s, prefix) {
		return ErrBadCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, prefix))
	if err != nil {
		return ErrBadCursor
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return ErrBadCursor
	}
	return nil
}

// ListTurnsPage implements Store.
func (s *conversationStore) ListTurnsPage(ctx context.Context, threadID string, filter PaginationFilter) (TurnPage, error) {
	limit := clampLimit(filter.Limit)
	afterSeq := int64(-1)
	if filter.Cursor != "" {
		var cur turnCursorPayload
		if err := decodeCursor(filter.Cursor, cursorKindTurn, &cur); err != nil {
			return TurnPage{}, err
		}
		if cur.ThreadID != threadID {
			return TurnPage{}, ErrBadCursor
		}
		afterSeq = cur.Seq
	}

	rows, err := listRows(ctx, s.db,
		`SELECT id, thread_id, seq, role, created_at FROM `+tableTurn+
			` WHERE thread_id = ? AND seq > ? ORDER BY seq ASC LIMIT ?`,
		[]any{threadID, afterSeq, limit + 1}, scanTurn, "turns page")
	if err != nil {
		return TurnPage{}, err
	}

	page := TurnPage{Turns: rows}
	if len(rows) > limit {
		page.Turns = rows[:limit]
		nc, err := encodeCursor(cursorKindTurn, turnCursorPayload{ThreadID: threadID, Seq: page.Turns[limit-1].Seq})
		if err != nil {
			return TurnPage{}, err
		}
		page.NextCursor = nc
	}
	return page, nil
}

// ListThreadsPage implements Store.
func (s *conversationStore) ListThreadsPage(ctx context.Context, filter PaginationFilter) (ThreadPage, error) {
	limit := clampLimit(filter.Limit)
	afterCreated, afterID := int64(-1), ""
	if filter.Cursor != "" {
		var cur threadCursorPayload
		if err := decodeCursor(filter.Cursor, cursorKindThread, &cur); err != nil {
			return ThreadPage{}, err
		}
		afterCreated, afterID = cur.CreatedAt, cur.ID
	}

	query := `SELECT t.id, t.name, t.created_at FROM ` + tableThread + ` t `
	if filter.IncludeArchived {
		query += `WHERE `
	} else {
		query += `LEFT JOIN ` + tableThreadArchive + ` a ON a.thread_id = t.id WHERE a.thread_id IS NULL AND `
	}
	query += `(t.created_at > ? OR (t.created_at = ? AND t.id > ?)) ORDER BY t.created_at ASC, t.id ASC LIMIT ?`

	rows, err := listRows(ctx, s.db, query, []any{afterCreated, afterCreated, afterID, limit + 1}, scanThread, "threads page")
	if err != nil {
		return ThreadPage{}, err
	}

	page := ThreadPage{Threads: rows}
	if len(rows) > limit {
		page.Threads = rows[:limit]
		last := page.Threads[limit-1]
		nc, err := encodeCursor(cursorKindThread, threadCursorPayload{CreatedAt: last.CreatedAt, ID: last.ID})
		if err != nil {
			return ThreadPage{}, err
		}
		page.NextCursor = nc
	}
	return page, nil
}

// Adapter-level pagination surface: S-43.T2's Adapter gains these two
// operations (ticket text: "no new CLI or MCP surface", so neither is
// registered on the JSON-RPC registry) for the future cascade_cpa_history
// MCP tool (P1-E20-W5-S43-T4, not yet built -- see this ticket's journal)
// to call once it lands.

// ListTurnsPage exposes Store.ListTurnsPage through the adapter surface.
func (a *Adapter) ListTurnsPage(ctx context.Context, threadID string, filter PaginationFilter) (TurnPage, error) {
	return a.store.ListTurnsPage(ctx, threadID, filter)
}

// ListThreadsPage exposes Store.ListThreadsPage through the adapter surface.
func (a *Adapter) ListThreadsPage(ctx context.Context, filter PaginationFilter) (ThreadPage, error) {
	return a.store.ListThreadsPage(ctx, filter)
}

func scanTurn(rows *sql.Rows) (Turn, error) {
	var t Turn
	var role string
	if err := rows.Scan(&t.ID, &t.ThreadID, &t.Seq, &role, &t.CreatedAt); err != nil {
		return Turn{}, cascade.Wrap(cascade.KindUnavailable, err, "conversation: scan turn")
	}
	r, err := DecodeRole(role)
	t.Role = r
	return t, err
}

func scanSegment(rows *sql.Rows) (Segment, error) {
	var seg Segment
	var kind string
	if err := rows.Scan(&seg.ID, &seg.TurnID, &seg.Seq, &kind, &seg.Content, &seg.CreatedAt); err != nil {
		return Segment{}, cascade.Wrap(cascade.KindUnavailable, err, "conversation: scan segment")
	}
	k, err := DecodeSegmentKind(kind)
	seg.Kind = k
	return seg, err
}

func scanThread(rows *sql.Rows) (Thread, error) {
	var t Thread
	if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt); err != nil {
		return Thread{}, cascade.Wrap(cascade.KindUnavailable, err, "conversation: scan thread")
	}
	return t, nil
}

// listRows runs query/args and decodes every row with scan; shared by
// ListTurns/ListSegments/ListThreads (store.go) and ListTurnsPage/
// ListThreadsPage (above) so the plumbing exists once.
func listRows[T any](ctx context.Context, db *sql.DB, query string, args []any, scan func(*sql.Rows) (T, error), what string) ([]T, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "conversation: list %s", what)
	}
	defer func() { _ = rows.Close() }()

	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "conversation: iterate %s", what)
	}
	return out, nil
}
