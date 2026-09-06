package scope

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeGitRoot returns root unconditionally, so tests never spawn a real
// git subprocess (Art.7.2 -- real git is exercised only by the Article-2
// integration suite).
func fakeGitRoot(root string) GitRootFunc {
	return func(_ context.Context, _ string) string { return root }
}

func TestResolveSessionScopeRequiresStore(t *testing.T) {
	_, err := ResolveSessionScope(context.Background(), ResolveDeps{}, ResolveInput{Cwd: "/x"})
	if err == nil {
		t.Fatal("ResolveSessionScope with nil Store = nil, want error")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("kind = %v, ok=%v, want KindInvalidInput", kind, ok)
	}
}

// TestResolveSessionScopeGeneralScope proves the R-16.3 restricted result:
// an unresolved cwd (no repository bound to the resolved git root) returns
// a SUCCESSFUL ScopeKindGeneral scope, never an error and never a
// fallback to an unscoped/global query.
func TestResolveSessionScopeGeneralScope(t *testing.T) {
	store := newTestStore(t)
	got, err := ResolveSessionScope(context.Background(), ResolveDeps{Store: store, GitRoot: fakeGitRoot("/nowhere")}, ResolveInput{
		Cwd: "/nowhere", User: "u1", Machine: "m1", Session: "s1", Branch: "main", Task: "t1",
	})
	if err != nil {
		t.Fatalf("ResolveSessionScope: %v", err)
	}
	if got.Kind != ScopeKindGeneral {
		t.Errorf("Kind = %q, want %q", got.Kind, ScopeKindGeneral)
	}
	if got.User != "u1" || got.Machine != "m1" || got.Cwd != "/nowhere" || got.Session != "s1" {
		t.Errorf("general scope dropped a preserved field: %+v", got)
	}
	if got.Project != "" || got.Workspace != "" || got.Product != "" || got.Repository != nil ||
		got.PackagePath != "" || got.Branch != "" || got.Task != "" {
		t.Errorf("general scope carries project-adjacent state: %+v", got)
	}
}

func TestResolveSessionScopeResolvedChain(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	if err := store.PutRepository(ctx, RepositoryRecord{ID: "repo-1", Remote: "git@example.com:a/b.git", PathHash: "hash1"}); err != nil {
		t.Fatalf("PutRepository: %v", err)
	}
	if err := store.PutRepoPath(ctx, RepoPathRecord{RepositoryID: "repo-1", RootPath: "/root"}); err != nil {
		t.Fatalf("PutRepoPath: %v", err)
	}
	project := Ref{Kind: ScopeKindProject, ID: "repo-1"}
	if err := store.PutEdge(ctx, Edge{From: project, To: Ref{Kind: ScopeKindWorkspace, ID: "ws-1"}, Kind: EdgeKindMemberOf}); err != nil {
		t.Fatalf("PutEdge project->workspace: %v", err)
	}
	if err := store.PutEdge(ctx, Edge{From: project, To: Ref{Kind: ScopeKindProduct, ID: "prod-1"}, Kind: EdgeKindMemberOf}); err != nil {
		t.Fatalf("PutEdge project->product: %v", err)
	}

	got, err := ResolveSessionScope(ctx, ResolveDeps{Store: store, GitRoot: fakeGitRoot("/root")}, ResolveInput{
		Cwd: "/root/pkg/sub", User: "u1", Machine: "m1", Branch: "feature", Task: "task-1", Session: "sess-1",
	})
	if err != nil {
		t.Fatalf("ResolveSessionScope: %v", err)
	}
	if got.Kind != ScopeKindSession {
		t.Errorf("Kind = %q, want %q", got.Kind, ScopeKindSession)
	}
	if got.Project != "repo-1" || got.Workspace != "ws-1" || got.Product != "prod-1" {
		t.Errorf("resolved chain = project=%q workspace=%q product=%q, want repo-1/ws-1/prod-1", got.Project, got.Workspace, got.Product)
	}
	if got.Repository == nil || got.Repository.ID != "repo-1" {
		t.Errorf("Repository = %+v, want ID=repo-1", got.Repository)
	}
	if got.PackagePath != "pkg/sub" {
		t.Errorf("PackagePath = %q, want %q", got.PackagePath, "pkg/sub")
	}
	if got.Branch != "feature" || got.Task != "task-1" || got.Session != "sess-1" {
		t.Errorf("caller-supplied fields not preserved: %+v", got)
	}
}

func TestScopeChainGeneralIsEmpty(t *testing.T) {
	chain := Chain(SessionScope{Kind: ScopeKindGeneral, Session: "s1"})
	if len(chain) != 0 {
		t.Errorf("Chain(general) = %+v, want empty", chain)
	}
}

func TestScopeChainOrder(t *testing.T) {
	s := SessionScope{
		Kind: ScopeKindSession, Session: "s1", Task: "t1", Project: "p1", Workspace: "w1", Product: "pr1",
	}
	chain := Chain(s)
	want := []Ref{
		{Kind: ScopeKindSession, ID: "s1"},
		{Kind: ScopeKindTask, ID: "t1"},
		{Kind: ScopeKindProject, ID: "p1"},
		{Kind: ScopeKindWorkspace, ID: "w1"},
		{Kind: ScopeKindProduct, ID: "pr1"},
	}
	if len(chain) != len(want) {
		t.Fatalf("Chain length = %d, want %d (%+v)", len(chain), len(want), chain)
	}
	for i := range want {
		if chain[i] != want[i] {
			t.Errorf("Chain[%d] = %+v, want %+v", i, chain[i], want[i])
		}
	}
}

// TestDenyByDefaultCandidateSet is R-16.4's leak fixture: a Project1
// session never receives a Project3 record absent a declared edge, and
// gains it only once an allowed edge is persisted.
func TestDenyByDefaultCandidateSet(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	project1 := Ref{Kind: ScopeKindProject, ID: "project1"}
	project3 := Ref{Kind: ScopeKindProject, ID: "project3"}
	chain := []Ref{project1}

	before, err := CandidateScopeRefs(ctx, store, chain)
	if err != nil {
		t.Fatalf("CandidateScopeRefs: %v", err)
	}
	for _, ref := range before {
		if ref == project3 {
			t.Fatalf("CandidateScopeRefs leaked project3 with no declared edge: %+v", before)
		}
	}

	if err := store.PutEdge(ctx, Edge{From: project1, To: project3, Kind: EdgeKindSharesContextWith}); err != nil {
		t.Fatalf("PutEdge: %v", err)
	}
	after, err := CandidateScopeRefs(ctx, store, chain)
	if err != nil {
		t.Fatalf("CandidateScopeRefs after edge: %v", err)
	}
	found := false
	for _, ref := range after {
		if ref == project3 {
			found = true
		}
	}
	if !found {
		t.Errorf("CandidateScopeRefs after declared edge = %+v, want project3 present", after)
	}
}

func TestCandidateScopeRefsDeduplicatesChain(t *testing.T) {
	store := newTestStore(t)
	ref := Ref{Kind: ScopeKindProject, ID: "p1"}
	out, err := CandidateScopeRefs(context.Background(), store, []Ref{ref, ref})
	if err != nil {
		t.Fatalf("CandidateScopeRefs: %v", err)
	}
	if len(out) != 1 {
		t.Errorf("CandidateScopeRefs duplicate chain entries = %+v, want exactly one", out)
	}
}

func TestCandidateScopeRefsRequiresStore(t *testing.T) {
	_, err := CandidateScopeRefs(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("CandidateScopeRefs with nil store = nil, want error")
	}
}
