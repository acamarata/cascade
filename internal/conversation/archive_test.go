package conversation

// Purpose: archive.go's own tests -- archiving excludes a thread from
//   ListThreadsPage's default listing while its turns remain readable and
//   searchable, an include-archived listing still returns it, unarchive
//   restores default visibility, and every operation is asserted against
//   real store state (never an emitted-event proxy).
// SPORT: internal.conversation.archive/ADDED (tests) (P1-E20-W5-S44-T3).

import (
	"context"
	"testing"
)

func TestArchiveThread_ExcludedFromDefaultListing_RetainedAndSearchable(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	turn := Turn{ID: NewTurnID("th-arch", 0, RoleUser), ThreadID: "th-arch", Seq: 0, Role: RoleUser, CreatedAt: 10}
	if err := s.AppendTurn(ctx, turn); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	appendSegment(t, s, turn, 0, "archived content stays findable")

	if archived, err := s.IsArchived(ctx, "th-arch"); err != nil || archived {
		t.Fatalf("IsArchived before archiving = %v, %v, want false, nil", archived, err)
	}
	if err := s.ArchiveThread(ctx, "th-arch", 100); err != nil {
		t.Fatalf("ArchiveThread: %v", err)
	}
	if archived, err := s.IsArchived(ctx, "th-arch"); err != nil || !archived {
		t.Fatalf("IsArchived after archiving = %v, %v, want true, nil", archived, err)
	}

	// STORE STATE, not an event: the default listing genuinely omits it.
	page, err := s.ListThreadsPage(ctx, PaginationFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListThreadsPage: %v", err)
	}
	if len(page.Threads) != 0 {
		t.Fatalf("ListThreadsPage default = %+v, want the archived thread excluded", page.Threads)
	}

	// Turn data is unchanged: still present, still readable.
	turns, err := s.ListTurns(ctx, "th-arch")
	if err != nil || len(turns) != 1 || turns[0].ID != turn.ID {
		t.Fatalf("ListTurns after archiving = %+v, %v, want the turn retained unchanged", turns, err)
	}

	// Searchable despite being archived.
	hits, err := s.SearchTurns(ctx, "findable", SearchFilter{})
	if err != nil || len(hits) != 1 {
		t.Fatalf("SearchTurns on archived thread = %+v, %v, want 1 hit", hits, err)
	}

	// Include-archived listing still returns it.
	includeAll, err := s.ListThreadsPage(ctx, PaginationFilter{Limit: 10, IncludeArchived: true})
	if err != nil || len(includeAll.Threads) != 1 || includeAll.Threads[0].ID != "th-arch" {
		t.Fatalf("ListThreadsPage(IncludeArchived) = %+v, %v, want [th-arch]", includeAll.Threads, err)
	}
}

func TestUnarchiveThread_RestoresDefaultVisibility(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	turn := Turn{ID: NewTurnID("th-unarch", 0, RoleUser), ThreadID: "th-unarch", Seq: 0, Role: RoleUser, CreatedAt: 10}
	if err := s.AppendTurn(ctx, turn); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	if err := s.ArchiveThread(ctx, "th-unarch", 100); err != nil {
		t.Fatalf("ArchiveThread: %v", err)
	}
	if err := s.UnarchiveThread(ctx, "th-unarch"); err != nil {
		t.Fatalf("UnarchiveThread: %v", err)
	}
	if archived, err := s.IsArchived(ctx, "th-unarch"); err != nil || archived {
		t.Fatalf("IsArchived after unarchive = %v, %v, want false, nil", archived, err)
	}
	page, err := s.ListThreadsPage(ctx, PaginationFilter{Limit: 10})
	if err != nil || len(page.Threads) != 1 || page.Threads[0].ID != "th-unarch" {
		t.Fatalf("ListThreadsPage after unarchive = %+v, %v, want [th-unarch] visible again", page.Threads, err)
	}

	// Unarchiving a never-archived thread is a documented no-op.
	if err := s.UnarchiveThread(ctx, "th-unarch"); err != nil {
		t.Fatalf("UnarchiveThread (already unarchived) = %v, want nil (no-op)", err)
	}
}

func TestArchiveThread_UnknownThreadNotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.ArchiveThread(context.Background(), "does-not-exist", 1); err == nil {
		t.Fatal("ArchiveThread(unknown thread) = nil error, want ErrThreadNotFound")
	}
}

// TestAdapterArchive_RoundTrip drives the Go-level Adapter surface.
func TestAdapterArchive_RoundTrip(t *testing.T) {
	adapter, registry, _ := newTestAdapter(t)
	if _, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		ThreadID: "th-adapter-arch", Role: "user", Segments: []appendSegmentWire{{Kind: "text", Content: "hi"}},
	}); errObj != nil {
		t.Fatalf("append_turn: %+v", errObj)
	}
	ctx := context.Background()
	if err := adapter.ArchiveThread(ctx, "th-adapter-arch"); err != nil {
		t.Fatalf("Adapter.ArchiveThread: %v", err)
	}
	page, err := adapter.ListThreadsPage(ctx, PaginationFilter{Limit: 10})
	if err != nil || len(page.Threads) != 0 {
		t.Fatalf("ListThreadsPage after Adapter.ArchiveThread = %+v, %v, want excluded", page.Threads, err)
	}
	if err := adapter.UnarchiveThread(ctx, "th-adapter-arch"); err != nil {
		t.Fatalf("Adapter.UnarchiveThread: %v", err)
	}
	page, err = adapter.ListThreadsPage(ctx, PaginationFilter{Limit: 10})
	if err != nil || len(page.Threads) != 1 {
		t.Fatalf("ListThreadsPage after Adapter.UnarchiveThread = %+v, %v, want restored", page.Threads, err)
	}
}

func TestArchiveUnarchive_EmptyThreadIDInvalid(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.ArchiveThread(ctx, "", 1); err == nil {
		t.Fatal("ArchiveThread(\"\") = nil error, want ErrInvalidRecord")
	}
	if err := s.UnarchiveThread(ctx, ""); err == nil {
		t.Fatal("UnarchiveThread(\"\") = nil error, want ErrInvalidRecord")
	}
}
