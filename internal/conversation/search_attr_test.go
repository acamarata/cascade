package conversation

// Purpose: SearchFilter's attribution narrowing (AttributedScope /
//   AttributedSet), the recall leg's principal half: it joins
//   conversation_thread_scope inside the MATCH statement, so it applies
//   before the limit, and an absent table or an empty scope matches nothing.
//   Driven through a real modernc-sqlite store (newTestStore).
// SPORT: internal.conversation.search/CHANGED (tests).

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// seedAttributedTurn creates thread id in project with attribution (empty:
// none) and one turn whose content repeats word n times, so a larger n
// ranks higher under bm25.
func seedAttributedTurn(t *testing.T, s Store, id, project, attribution string, n int) {
	t.Helper()
	ctx := context.Background()
	turn := Turn{ID: NewTurnID(id, 0, RoleUser), ThreadID: id, Role: RoleUser, CreatedAt: 5}
	content := strings.Repeat("needle ", n) + "filler text"
	if attribution == "" {
		seedProjectTurn(t, s, id, &project, content)
		return
	}
	if err := s.CreateThreadProject(ctx, id, provider.SensitivityPublic, attribution, &project, 5); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
	if err := s.AppendTurn(ctx, turn); err != nil {
		t.Fatalf("append %s: %v", id, err)
	}
	appendSegment(t, s, turn, 0, content)
}

// attributedFixture seeds one project where the principal's own thread
// ranks LAST behind an owner-attributed, an unattributed and a foreign
// principal's thread, plus the principal's thread in another project.
func attributedFixture(t *testing.T) Store {
	t.Helper()
	s := newTestStore(t)
	if err := ensureScopeSchema(context.Background(), s.(*conversationStore).db, newTestClock()); err != nil {
		t.Fatalf("ensureScopeSchema: %v", err)
	}
	seedAttributedTurn(t, s, "th-owner", "project:a", ownerScope, 5)
	seedAttributedTurn(t, s, "th-none", "project:a", "", 4)
	seedAttributedTurn(t, s, "th-foreign", "project:a", "project:b", 3)
	seedAttributedTurn(t, s, "th-elsewhere", "project:z", "project:a", 2)
	seedAttributedTurn(t, s, "th-own", "project:a", "project:a", 1)
	return s
}

func TestSearchTurns_AttributedScopeNarrowsInSQLBeforeLimit(t *testing.T) {
	s := attributedFixture(t)
	ctx := context.Background()
	f := SearchFilter{ProjectRef: "project:a", ProjectSet: true, AttributedScope: "project:a", AttributedSet: true, Limit: 1}
	hits, err := s.SearchTurns(ctx, "needle", f)
	if err != nil || hitThreads(hits) != "th-own" {
		t.Fatalf("attributed hits = %q, %v; want only th-own", hitThreads(hits), err)
	}
	f.AttributedSet, f.AttributedScope = false, ""
	control, err := s.SearchTurns(ctx, "needle", f)
	if err != nil || hitThreads(control) != "th-owner" {
		t.Fatalf("project-only hits = %q, %v; want th-owner first (the fixture is not vacuous)", hitThreads(control), err)
	}
	f = SearchFilter{AttributedScope: "project:a"}
	half, err := s.SearchTurns(ctx, "needle", f)
	if err != nil || hitThreads(half) != "th-elsewhere,th-own" {
		t.Fatalf("half-set attribution hits = %q, %v; want th-elsewhere,th-own (a scope alone still narrows)", hitThreads(half), err)
	}
}

func TestSearchTurns_EmptyAttributedScopeMatchesNothing(t *testing.T) {
	s := attributedFixture(t)
	hits, err := s.SearchTurns(context.Background(), "needle", SearchFilter{AttributedSet: true})
	if err != nil || len(hits) != 0 {
		t.Fatalf("empty attributed scope hits = %q, %v; want none", hitThreads(hits), err)
	}
}

func TestSearchTurns_AttributedWithoutScopeTableMatchesNothing(t *testing.T) {
	s := newTestStore(t)
	seedProjectTurn(t, s, "th-a", strp("project:a"), "needle here")
	hits, err := s.SearchTurns(context.Background(), "needle", SearchFilter{AttributedScope: "project:a", AttributedSet: true})
	if err != nil || len(hits) != 0 {
		t.Fatalf("hits without a scope table = %q, %v; want none and no error", hitThreads(hits), err)
	}
	all, err := s.SearchTurns(context.Background(), "needle", SearchFilter{})
	if err != nil || len(all) != 1 {
		t.Fatalf("unnarrowed hits = %d, %v; want 1 (the fixture is not vacuous)", len(all), err)
	}
}
