package conversation

// Purpose: pagination.go's own tests -- first/interior/last page
//   boundaries, next-cursor nil at end-of-results, bad-cursor refusal
//   (never a panic), and cross-thread cursor rejection, all driven
//   through the real ConversationStore/Adapter entry points against a
//   real modernc-sqlite database (newTestStore, domain_test.go).
// SPORT: internal.conversation.pagination/ADDED (tests) (P1-E20-W5-S44-T3).

import (
	"context"
	"testing"
)

// seedTurns appends n turns (each with no segments) to threadID and
// returns them in insertion order.
func seedTurns(t *testing.T, s Store, threadID string, n int) []Turn {
	t.Helper()
	ctx := context.Background()
	turns := make([]Turn, 0, n)
	for i := 0; i < n; i++ {
		turn := Turn{ID: NewTurnID(threadID, int64(i), RoleUser), ThreadID: threadID, Seq: int64(i), Role: RoleUser, CreatedAt: int64(10 + i)}
		if err := s.AppendTurn(ctx, turn); err != nil {
			t.Fatalf("seedTurns: AppendTurn(%d): %v", i, err)
		}
		turns = append(turns, turn)
	}
	return turns
}

func TestPaginationTurns_FirstInteriorLastPage(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	want := seedTurns(t, s, "th-page", 5)

	// First page: limit 2 -> turns[0], turns[1], NextCursor set.
	page1, err := s.ListTurnsPage(ctx, "th-page", PaginationFilter{Limit: 2})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1.Turns) != 2 || page1.Turns[0].ID != want[0].ID || page1.Turns[1].ID != want[1].ID {
		t.Fatalf("page1.Turns = %+v, want [%s, %s]", page1.Turns, want[0].ID, want[1].ID)
	}
	if page1.NextCursor == "" {
		t.Fatal("page1.NextCursor = \"\", want a cursor (more rows remain)")
	}

	// Interior page: resumes at turns[2], turns[3].
	page2, err := s.ListTurnsPage(ctx, "th-page", PaginationFilter{Cursor: page1.NextCursor, Limit: 2})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2.Turns) != 2 || page2.Turns[0].ID != want[2].ID || page2.Turns[1].ID != want[3].ID {
		t.Fatalf("page2.Turns = %+v, want [%s, %s]", page2.Turns, want[2].ID, want[3].ID)
	}
	if page2.NextCursor == "" {
		t.Fatal("page2.NextCursor = \"\", want a cursor (one row remains)")
	}

	// Last page: turns[4] only, NextCursor must be nil/empty.
	page3, err := s.ListTurnsPage(ctx, "th-page", PaginationFilter{Cursor: page2.NextCursor, Limit: 2})
	if err != nil {
		t.Fatalf("page3: %v", err)
	}
	if len(page3.Turns) != 1 || page3.Turns[0].ID != want[4].ID {
		t.Fatalf("page3.Turns = %+v, want [%s]", page3.Turns, want[4].ID)
	}
	if page3.NextCursor != "" {
		t.Fatalf("page3.NextCursor = %q, want \"\" on the last page", page3.NextCursor)
	}
}

func TestPaginationTurns_EmptyResultPage(t *testing.T) {
	s := newTestStore(t)
	page, err := s.ListTurnsPage(context.Background(), "th-empty", PaginationFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListTurnsPage(empty thread): %v", err)
	}
	if len(page.Turns) != 0 || page.NextCursor != "" {
		t.Fatalf("page = %+v, want zero turns and no cursor", page)
	}
}

func TestPaginationTurns_BadCursorErrorsNotPanic(t *testing.T) {
	s := newTestStore(t)
	seedTurns(t, s, "th-bad", 1)
	ctx := context.Background()

	for name, cur := range map[string]Cursor{
		"empty-after-prefix": "t.",
		"not-base64":         "t.@@@not-base64@@@",
		"wrong-kind-prefix":  "h.not-a-turn-cursor",
		"garbage":            "not-a-cursor-at-all",
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("ListTurnsPage panicked on cursor %q: %v", cur, r)
				}
			}()
			if _, err := s.ListTurnsPage(ctx, "th-bad", PaginationFilter{Cursor: cur}); err == nil {
				t.Fatalf("ListTurnsPage(cursor=%q) = nil error, want ErrBadCursor", cur)
			}
		})
	}
}

func TestPaginationTurns_CrossThreadCursorRejected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedTurns(t, s, "th-a", 3)
	seedTurns(t, s, "th-b", 3)

	pageA, err := s.ListTurnsPage(ctx, "th-a", PaginationFilter{Limit: 1})
	if err != nil || pageA.NextCursor == "" {
		t.Fatalf("pageA = %+v, %v, want a cursor from th-a", pageA, err)
	}
	if _, err := s.ListTurnsPage(ctx, "th-b", PaginationFilter{Cursor: pageA.NextCursor}); err == nil {
		t.Fatal("ListTurnsPage(th-b, cursor from th-a) = nil error, want ErrBadCursor")
	}
}

func TestPaginationThreads_FirstAndLastPageIncludeArchivedDefault(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for i, name := range []string{"th1", "th2", "th3"} {
		turn := Turn{ID: NewTurnID(name, 0, RoleUser), ThreadID: name, Seq: 0, Role: RoleUser, CreatedAt: int64(10 + i)}
		if err := s.AppendTurn(ctx, turn); err != nil {
			t.Fatalf("seed thread %s: %v", name, err)
		}
	}
	if err := s.ArchiveThread(ctx, "th2", 50); err != nil {
		t.Fatalf("ArchiveThread: %v", err)
	}

	page, err := s.ListThreadsPage(ctx, PaginationFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListThreadsPage: %v", err)
	}
	if len(page.Threads) != 2 {
		t.Fatalf("ListThreadsPage default (archived excluded) = %d threads, want 2", len(page.Threads))
	}
	for _, th := range page.Threads {
		if th.ID == "th2" {
			t.Fatalf("ListThreadsPage default listing includes archived thread %q", th.ID)
		}
	}

	all, err := s.ListThreadsPage(ctx, PaginationFilter{Limit: 10, IncludeArchived: true})
	if err != nil {
		t.Fatalf("ListThreadsPage(IncludeArchived): %v", err)
	}
	if len(all.Threads) != 3 {
		t.Fatalf("ListThreadsPage(IncludeArchived) = %d threads, want 3", len(all.Threads))
	}
}

// TestAdapterPagination_RoundTrip drives the Go-level Adapter surface
// (pagination.go's ListTurnsPage/ListThreadsPage) rather than the raw
// Store, proving the adapter wrapper genuinely delegates, using
// adapter_test.go's own newTestAdapter/dispatch helpers (same package).
func TestAdapterPagination_RoundTrip(t *testing.T) {
	adapter, registry, _ := newTestAdapter(t)
	ctx := context.Background()
	if err := adapter.ArchiveThread(ctx, "no-such-thread"); err == nil {
		t.Fatal("Adapter.ArchiveThread(no-such-thread) = nil error, want ErrThreadNotFound")
	}

	if _, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		ThreadID: "th-adapter-page", Role: "user", Segments: []appendSegmentWire{{Kind: "text", Content: "hi"}},
	}); errObj != nil {
		t.Fatalf("append_turn: %+v", errObj)
	}

	turnPage, err := adapter.ListTurnsPage(ctx, "th-adapter-page", PaginationFilter{Limit: 10})
	if err != nil || len(turnPage.Turns) != 1 {
		t.Fatalf("Adapter.ListTurnsPage = %+v, %v, want 1 turn", turnPage, err)
	}
	threadPage, err := adapter.ListThreadsPage(ctx, PaginationFilter{Limit: 10})
	if err != nil || len(threadPage.Threads) != 1 {
		t.Fatalf("Adapter.ListThreadsPage = %+v, %v, want 1 thread", threadPage, err)
	}
}

// TestPaginationThreads_MultiPageWithCursor exercises ListThreadsPage's
// own interior-page cursor path (decode + re-encode), the twin of
// TestPaginationTurns_FirstInteriorLastPage for threads.
func TestPaginationThreads_MultiPageWithCursor(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for i, name := range []string{"th-p1", "th-p2", "th-p3", "th-p4"} {
		turn := Turn{ID: NewTurnID(name, 0, RoleUser), ThreadID: name, Seq: 0, Role: RoleUser, CreatedAt: int64(10 + i)}
		if err := s.AppendTurn(ctx, turn); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	page1, err := s.ListThreadsPage(ctx, PaginationFilter{Limit: 2})
	if err != nil || len(page1.Threads) != 2 || page1.NextCursor == "" {
		t.Fatalf("page1 = %+v, %v, want 2 threads with a next cursor", page1, err)
	}
	if page1.Threads[0].ID != "th-p1" || page1.Threads[1].ID != "th-p2" {
		t.Fatalf("page1.Threads = %+v, want [th-p1, th-p2]", page1.Threads)
	}

	page2, err := s.ListThreadsPage(ctx, PaginationFilter{Cursor: page1.NextCursor, Limit: 2})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2.Threads) != 2 || page2.Threads[0].ID != "th-p3" || page2.Threads[1].ID != "th-p4" {
		t.Fatalf("page2.Threads = %+v, want [th-p3, th-p4]", page2.Threads)
	}
	if page2.NextCursor != "" {
		t.Fatalf("page2.NextCursor = %q, want \"\" on the last page", page2.NextCursor)
	}
}

// TestNewStoreMethods_ClosedDBRefusesNotPanics drives every new Store
// method added by this ticket against a store whose underlying db has
// already been closed: a real, unforced database failure (never a
// synthetic fault double) that exercises each method's generic
// KindUnavailable error-wrap branch and proves none of them panic on it.
func TestNewStoreMethods_ClosedDBRefusesNotPanics(t *testing.T) {
	s, db := newTestStoreWithDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close: %v", err)
	}
	ctx := context.Background()

	calls := map[string]func() error{
		"ListTurnsPage": func() error { _, err := s.ListTurnsPage(ctx, "th", PaginationFilter{}); return err },
		"ListThreadsPage": func() error {
			_, err := s.ListThreadsPage(ctx, PaginationFilter{})
			return err
		},
		"SearchTurns":     func() error { _, err := s.SearchTurns(ctx, "x", SearchFilter{}); return err },
		"ArchiveThread":   func() error { return s.ArchiveThread(ctx, "th", 1) },
		"UnarchiveThread": func() error { return s.UnarchiveThread(ctx, "th") },
		"IsArchived":      func() error { _, err := s.IsArchived(ctx, "th"); return err },
		"PruneTurns": func() error {
			_, err := s.PruneTurns(ctx, RetentionPolicy{AgeMaxDays: 1, TurnsMaxPerThread: 1}, 100)
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%s panicked on a closed db: %v", name, r)
				}
			}()
			if err := call(); err == nil {
				t.Fatalf("%s on a closed db = nil error, want a refusal", name)
			}
		})
	}
}
