package conversation

// Purpose: SearchTurns narrowed by a thread's recorded project (project.go,
//   search.go): the join runs in SQL before the limit, the general bucket ""
//   is its own value, and a thread with no project row matches no narrowed
//   query. Real sqlite store, seeded through AppendFirstTurnProject.
// SPORT: internal.conversation.project/ADDED (tests) (P1-CPA-06).

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// seedProjectTurn creates thread id (project nil: no row) with one turn.
func seedProjectTurn(t *testing.T, s Store, id string, project *string, content string) {
	t.Helper()
	turn := Turn{ID: NewTurnID(id, 0, RoleUser), ThreadID: id, Role: RoleUser, CreatedAt: 5}
	seg := Segment{ID: NewSegmentID(turn.ID, 0, SegmentText), TurnID: turn.ID, Kind: SegmentText, Content: content, CreatedAt: 5}
	if err := s.AppendFirstTurnProject(context.Background(), provider.SensitivityRestricted, turn, []Segment{seg}, project); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func strp(s string) *string { return &s }

func hitThreads(hits []TurnMatch) string {
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.Turn.ThreadID
	}
	return strings.Join(ids, ",")
}

// TestSearchTurns_GeneralBucketMatchesOnlyGeneral: ProjectRef "" with
// ProjectSet matches exactly the "" rows, never a project row or a thread
// without a row.
func TestSearchTurns_GeneralBucketMatchesOnlyGeneral(t *testing.T) {
	s := newTestStore(t)
	seedProjectTurn(t, s, "th-gen", strp(""), "findme general")
	seedProjectTurn(t, s, "th-a", strp("project:a"), "findme alpha")
	seedProjectTurn(t, s, "th-none", nil, "findme unscoped")
	hits, err := s.SearchTurns(context.Background(), "findme", SearchFilter{ProjectSet: true})
	if err != nil || hitThreads(hits) != "th-gen" {
		t.Fatalf("general bucket hits = %q, %v; want only th-gen", hitThreads(hits), err)
	}
	all, err := s.SearchTurns(context.Background(), "findme", SearchFilter{})
	if err != nil || len(all) != 3 {
		t.Fatalf("unnarrowed hits = %d, %v; want all 3 (the fixture is not vacuous)", len(all), err)
	}
}

// TestSearchTurns_NarrowsByScopeRefInSQL: 60 project-B turns rank above one
// project-A turn; with Limit 5 the A turn is still returned because the
// project join runs before the limit. Failing input: filtering after the limit.
func TestSearchTurns_NarrowsByScopeRefInSQL(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 60; i++ {
		seedProjectTurn(t, s, fmt.Sprintf("th-b%02d", i), strp("project:b"), "needle needle needle")
	}
	seedProjectTurn(t, s, "th-a", strp("project:a"), "needle among many other unrelated filler words here")
	ctx := context.Background()
	top, err := s.SearchTurns(ctx, "needle", SearchFilter{Limit: 5})
	if err != nil || len(top) != 5 || strings.Contains(hitThreads(top), "th-a") {
		t.Fatalf("unnarrowed top 5 = %q, %v; want only project-b threads", hitThreads(top), err)
	}
	hits, err := s.SearchTurns(ctx, "needle", SearchFilter{Limit: 5, ProjectRef: "project:a", ProjectSet: true})
	if err != nil || hitThreads(hits) != "th-a" {
		t.Fatalf("project:a hits = %q, %v; want only th-a", hitThreads(hits), err)
	}
}

// TestSearchTurns_UnscopedThreadNeverMatchesScopedQuery: a thread with no
// row matches no narrowed query, including one whose ProjectRef is set
// without ProjectSet. Failing input: a LEFT JOIN or an unnarrowed fallback.
func TestSearchTurns_UnscopedThreadNeverMatchesScopedQuery(t *testing.T) {
	s := newTestStore(t)
	seedProjectTurn(t, s, "th-none", nil, "findme unscoped")
	ctx := context.Background()
	for name, f := range map[string]SearchFilter{
		"project":      {ProjectRef: "project:a", ProjectSet: true},
		"general":      {ProjectSet: true},
		"ref-only":     {ProjectRef: "project:a"},
		"thread+scope": {ThreadID: "th-none", ProjectRef: "project:a", ProjectSet: true},
	} {
		if hits, err := s.SearchTurns(ctx, "findme", f); err != nil || len(hits) != 0 {
			t.Fatalf("%s: unscoped thread matched %q, %v; want no hits", name, hitThreads(hits), err)
		}
	}
	if hits, _ := s.SearchTurns(ctx, "findme", SearchFilter{ThreadID: "th-none"}); len(hits) != 1 {
		t.Fatal("the unscoped thread is not searchable at all: fixture is vacuous")
	}
}
