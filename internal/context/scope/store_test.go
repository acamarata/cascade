package scope

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

func newTestStore(t testHelper) *GraphStore {
	t.Helper()
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyScopeSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyScopeSchema: %v", err)
	}
	return NewGraphStore(db)
}

func TestPutAndGetRepositoryForRoot(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	rec := RepositoryRecord{ID: "repo-1", Remote: "git@example.com:acamarata/cascade.git", PathHash: "abc123"}
	if err := s.PutRepository(ctx, rec); err != nil {
		t.Fatalf("PutRepository: %v", err)
	}
	if err := s.PutRepoPath(ctx, RepoPathRecord{RepositoryID: "repo-1", RootPath: "/repo/root"}); err != nil {
		t.Fatalf("PutRepoPath: %v", err)
	}
	got, ok, err := s.RepositoryForRoot(ctx, "/repo/root")
	if err != nil || !ok {
		t.Fatalf("RepositoryForRoot: got=%+v ok=%v err=%v", got, ok, err)
	}
	if got.ID != "repo-1" || got.Remote != rec.Remote || got.PathHash != rec.PathHash {
		t.Errorf("RepositoryForRoot = %+v, want %+v", got, rec)
	}
}

func TestRepositoryForRootUnregisteredIsNoErrorNoOK(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, ok, err := s.RepositoryForRoot(ctx, "/never/registered")
	if err != nil {
		t.Fatalf("RepositoryForRoot unregistered: err=%v, want nil", err)
	}
	if ok {
		t.Error("RepositoryForRoot unregistered: ok=true, want false")
	}
}

func TestPutEdgeRejectsUnknownKind(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	err := s.PutEdge(ctx, ScopeEdge{
		From: ScopeRef{Kind: ScopeKindProject, ID: "p1"},
		To:   ScopeRef{Kind: ScopeKindWorkspace, ID: "w1"},
		Kind: EdgeKind("owns"),
	})
	if err == nil {
		t.Fatal("PutEdge with unknown kind = nil, want error")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("PutEdge unknown kind error kind = %v, ok=%v, want KindInvalidInput", kind, ok)
	}
}

func TestPutEdgeAndEdgeTargets(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	project := ScopeRef{Kind: ScopeKindProject, ID: "p1"}
	workspace := ScopeRef{Kind: ScopeKindWorkspace, ID: "w1"}
	if err := s.PutEdge(ctx, ScopeEdge{From: project, To: workspace, Kind: EdgeKindMemberOf}); err != nil {
		t.Fatalf("PutEdge: %v", err)
	}
	targets, err := s.EdgeTargets(ctx, project, EdgeKindMemberOf)
	if err != nil {
		t.Fatalf("EdgeTargets: %v", err)
	}
	if len(targets) != 1 || targets[0] != workspace {
		t.Errorf("EdgeTargets = %+v, want [%+v]", targets, workspace)
	}
	parents, err := s.ParentScopes(ctx, project)
	if err != nil {
		t.Fatalf("ParentScopes: %v", err)
	}
	if len(parents) != 1 || parents[0] != workspace {
		t.Errorf("ParentScopes = %+v, want [%+v]", parents, workspace)
	}
}

// TestPutEdgeRejectsCycle proves R-21.157's build-time cycle rejection: a
// member_of chain A->B->C, then an attempt to close the cycle C->A, is
// refused with an A-T7 typed invalid-input error rather than being
// silently stored (which would let a later traversal loop forever).
func TestPutEdgeRejectsCycle(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	a := ScopeRef{Kind: ScopeKindProject, ID: "a"}
	b := ScopeRef{Kind: ScopeKindWorkspace, ID: "b"}
	c := ScopeRef{Kind: ScopeKindProduct, ID: "c"}

	if err := s.PutEdge(ctx, ScopeEdge{From: a, To: b, Kind: EdgeKindMemberOf}); err != nil {
		t.Fatalf("PutEdge a->b: %v", err)
	}
	if err := s.PutEdge(ctx, ScopeEdge{From: b, To: c, Kind: EdgeKindMemberOf}); err != nil {
		t.Fatalf("PutEdge b->c: %v", err)
	}
	err := s.PutEdge(ctx, ScopeEdge{From: c, To: a, Kind: EdgeKindMemberOf})
	if err == nil {
		t.Fatal("PutEdge c->a (closes a cycle) = nil, want error")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("cycle rejection error kind = %v, ok=%v, want KindInvalidInput", kind, ok)
	}

	// The self-edge case is a one-node cycle and must also be refused.
	if err := s.PutEdge(ctx, ScopeEdge{From: a, To: a, Kind: EdgeKindMemberOf}); err == nil {
		t.Error("PutEdge self-cycle = nil, want error")
	}
}

// TestPutEdgeCycleCheckDoesNotApplyToRouteEdges proves route edges
// (depends_on, shares_context_with) are NOT cycle-checked -- a mutual
// dependency is a legitimate, non-hierarchical relationship.
func TestPutEdgeCycleCheckDoesNotApplyToRouteEdges(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	a := ScopeRef{Kind: ScopeKindProject, ID: "a"}
	b := ScopeRef{Kind: ScopeKindProject, ID: "b"}
	if err := s.PutEdge(ctx, ScopeEdge{From: a, To: b, Kind: EdgeKindDependsOn}); err != nil {
		t.Fatalf("PutEdge a->b depends_on: %v", err)
	}
	if err := s.PutEdge(ctx, ScopeEdge{From: b, To: a, Kind: EdgeKindDependsOn}); err != nil {
		t.Fatalf("PutEdge b->a depends_on (mutual, must be allowed): %v", err)
	}
}

func TestPutScopeRejectsInvalidRef(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.PutScope(ctx, ScopeGraphRecord{Ref: ScopeRef{Kind: ScopeKind("bogus"), ID: "x"}}); err == nil {
		t.Error("PutScope invalid kind = nil, want error")
	}
	if err := s.PutScope(ctx, ScopeGraphRecord{Ref: ScopeRef{Kind: ScopeKindProject, ID: ""}}); err == nil {
		t.Error("PutScope empty id = nil, want error")
	}
}

func TestPutRepositoryRequiresID(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.PutRepository(ctx, RepositoryRecord{}); err == nil {
		t.Error("PutRepository empty id = nil, want error")
	}
}

func TestPutRepoPathRequiresFields(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.PutRepoPath(ctx, RepoPathRecord{}); err == nil {
		t.Error("PutRepoPath empty fields = nil, want error")
	}
}

func TestRepositoryForRootCorruptRecordIsIntegrityError(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	// A repo_path row referencing a repository id that was never inserted
	// simulates a corrupt/missing record without needing to disable the
	// (absent) foreign-key enforcement.
	if err := s.PutRepoPath(ctx, RepoPathRecord{RepositoryID: "ghost", RootPath: "/root"}); err != nil {
		t.Fatalf("PutRepoPath: %v", err)
	}
	_, _, err := s.RepositoryForRoot(ctx, "/root")
	if err == nil {
		t.Fatal("RepositoryForRoot with dangling repository_id = nil, want error")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindIntegrity {
		t.Errorf("dangling repository_id error kind = %v, ok=%v, want KindIntegrity", kind, ok)
	}
}
