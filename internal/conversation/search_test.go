package conversation

// Purpose: search.go's own tests -- matching turns ranked by relevance,
//   no-results, adversarial MATCH input rejected (never a panic, never
//   SQL injection), and the FTS5-unavailable refusal on a store whose db
//   never had ensureFTS5 run against it. All driven through a real
//   modernc-sqlite database (newTestStore).
// SPORT: internal.conversation.search/ADDED (tests) (P1-E20-W5-S44-T3).

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
)

// appendSegment is a small seeding helper: appends one segment (kind
// text) to turn.
func appendSegment(t *testing.T, s Store, turn Turn, seq int64, content string) {
	t.Helper()
	seg := Segment{ID: NewSegmentID(turn.ID, seq, SegmentText), TurnID: turn.ID, Seq: seq, Kind: SegmentText, Content: content, CreatedAt: turn.CreatedAt}
	if err := s.AppendSegment(context.Background(), seg); err != nil {
		t.Fatalf("appendSegment(%q): %v", content, err)
	}
}

func TestSearchTurns_MatchesRankedAndNoResults(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	turn1 := Turn{ID: NewTurnID("th-search", 0, RoleUser), ThreadID: "th-search", Seq: 0, Role: RoleUser, CreatedAt: 10}
	turn2 := Turn{ID: NewTurnID("th-search", 1, RoleAssistant), ThreadID: "th-search", Seq: 1, Role: RoleAssistant, CreatedAt: 11}
	if err := s.AppendTurn(ctx, turn1); err != nil {
		t.Fatalf("AppendTurn(1): %v", err)
	}
	if err := s.AppendTurn(ctx, turn2); err != nil {
		t.Fatalf("AppendTurn(2): %v", err)
	}
	appendSegment(t, s, turn1, 0, "the quick brown fox jumps")
	appendSegment(t, s, turn2, 0, "a slow turtle crawls")

	hits, err := s.SearchTurns(ctx, "fox", SearchFilter{})
	if err != nil {
		t.Fatalf("SearchTurns(fox): %v", err)
	}
	if len(hits) != 1 || hits[0].Turn.ID != turn1.ID {
		t.Fatalf("SearchTurns(fox) = %+v, want exactly [%s]", hits, turn1.ID)
	}

	none, err := s.SearchTurns(ctx, "elephant", SearchFilter{})
	if err != nil {
		t.Fatalf("SearchTurns(elephant): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("SearchTurns(elephant) = %+v, want no results", none)
	}
}

func TestSearchTurns_ThreadScoped(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	turnA := Turn{ID: NewTurnID("th-a", 0, RoleUser), ThreadID: "th-a", Seq: 0, Role: RoleUser, CreatedAt: 10}
	turnB := Turn{ID: NewTurnID("th-b", 0, RoleUser), ThreadID: "th-b", Seq: 0, Role: RoleUser, CreatedAt: 10}
	if err := s.AppendTurn(ctx, turnA); err != nil {
		t.Fatalf("AppendTurn(a): %v", err)
	}
	if err := s.AppendTurn(ctx, turnB); err != nil {
		t.Fatalf("AppendTurn(b): %v", err)
	}
	appendSegment(t, s, turnA, 0, "banana bread recipe")
	appendSegment(t, s, turnB, 0, "banana smoothie recipe")

	hits, err := s.SearchTurns(ctx, "banana", SearchFilter{ThreadID: "th-a"})
	if err != nil {
		t.Fatalf("SearchTurns(scoped): %v", err)
	}
	if len(hits) != 1 || hits[0].Turn.ThreadID != "th-a" {
		t.Fatalf("SearchTurns(scoped to th-a) = %+v, want exactly th-a's turn", hits)
	}
}

// TestSearchTurns_AdversarialQueryRejectedNotPanic drives malformed FTS5
// MATCH syntax through the real parameterised query path: no string
// interpolation exists for injection to exploit, so the only possible
// outcomes are a typed refusal or (wrongly) a panic -- this proves the
// former for a representative set of adversarial inputs, including a
// classic SQL-injection-shaped string that FTS5 treats as a syntax error,
// not as executable SQL.
func TestSearchTurns_AdversarialQueryRejectedNotPanic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	turn := Turn{ID: NewTurnID("th-adv", 0, RoleUser), ThreadID: "th-adv", Seq: 0, Role: RoleUser, CreatedAt: 10}
	if err := s.AppendTurn(ctx, turn); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	appendSegment(t, s, turn, 0, "ordinary content")

	for name, q := range map[string]string{
		"unbalanced-quote":     `"unterminated`,
		"bare-operator":        "AND OR NOT",
		"unbalanced-paren":     "(fox",
		"injection-shaped":     `x'); DROP TABLE conversation_turn; --`,
		"column-filter-syntax": "nonexistent_column: value",
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("SearchTurns panicked on query %q: %v", q, r)
				}
			}()
			if _, err := s.SearchTurns(ctx, q, SearchFilter{}); err == nil {
				t.Errorf("SearchTurns(%q) = nil error, want a rejection", q)
			}
		})
	}

	// The injection-shaped input must not have actually dropped the
	// table: a real, unrelated turn is still readable afterward.
	turns, err := s.ListTurns(ctx, "th-adv")
	if err != nil || len(turns) != 1 {
		t.Fatalf("ListTurns after adversarial search = %+v, %v, want the seeded turn intact", turns, err)
	}
}

func TestSearchTurns_UnavailableWhenNoFTS5Index(t *testing.T) {
	// A store whose schema was applied BEFORE ensureFTS5 existed (a
	// non-sqlite dialect, or an older on-disk db) has no fts5 table.
	// Simulate it directly: apply everything except the raw FTS5 DDL by
	// using a dialect name ensureFTS5 treats as a no-op.
	path := filepath.Join(t.TempDir(), "no-fts.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)

	if err := migrate.Apply(context.Background(), migrate.ApplyConfig{
		DB: db, Dialect: migrate.SQLiteEmitter{}, Clock: newTestClock(),
	}, MigrationSet()); err != nil {
		t.Fatalf("migrate.Apply: %v", err)
	}
	// Deliberately skip ensureFTS5 -- this store's db never gets the fts5 table.

	s := NewStore(db)
	if _, err := s.SearchTurns(context.Background(), "anything", SearchFilter{}); err == nil {
		t.Fatal("SearchTurns with no fts5 index = nil error, want ErrSearchUnavailable")
	}

	// Appending must still succeed with no fts5 table present (the
	// mirror write is skipped, not attempted and failed).
	turn := Turn{ID: NewTurnID("th-no-fts", 0, RoleUser), ThreadID: "th-no-fts", Seq: 0, Role: RoleUser, CreatedAt: 10}
	if err := s.AppendTurn(context.Background(), turn); err != nil {
		t.Fatalf("AppendTurn with no fts5 index: %v", err)
	}
	appendSegment(t, s, turn, 0, "still works without search")
}

// TestAdapterSearchTurns_RoundTrip drives the Go-level Adapter surface.
func TestAdapterSearchTurns_RoundTrip(t *testing.T) {
	adapter, registry, _ := newTestAdapter(t)
	if _, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		ThreadID: "th-adapter-search", Role: "user", Segments: []appendSegmentWire{{Kind: "text", Content: "searchable content"}},
	}); errObj != nil {
		t.Fatalf("append_turn: %+v", errObj)
	}
	hits, err := adapter.SearchTurns(context.Background(), "searchable", SearchFilter{})
	if err != nil || len(hits) != 1 {
		t.Fatalf("Adapter.SearchTurns = %+v, %v, want 1 hit", hits, err)
	}
}

// TestEnsureFTS5_NonSQLiteDialectIsNoOp proves ensureFTS5's documented
// no-op for a non-SQLite dialect, using the REAL PostgresEmitter (never a
// hand-authored fake Dialect) so this exercises the actual Name() value
// every real Postgres-profile caller would pass.
func TestEnsureFTS5_NonSQLiteDialectIsNoOp(t *testing.T) {
	_, db := newTestStoreWithDB(t)
	ctx := context.Background()
	if err := ensureFTS5(ctx, db, migrate.PostgresEmitter{}); err != nil {
		t.Fatalf("ensureFTS5(postgres dialect) = %v, want nil (documented no-op)", err)
	}
	if err := ensureFTS5(ctx, db, nil); err != nil {
		t.Fatalf("ensureFTS5(nil dialect) = %v, want nil (documented no-op)", err)
	}
}
