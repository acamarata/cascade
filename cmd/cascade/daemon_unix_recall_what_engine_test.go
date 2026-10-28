//go:build !windows

// Purpose: recall.what's conversation leg end to end over a REAL sqlite
// conversation store, the real adapter and the real process egress engine
// (ForceFileVault custody via bindTestProcessFirewall): project narrowing
// and the principal's attribution narrowing both apply in SQL before the
// limit, restricted and local-only threads never cross the boundary, and
// over the composed daemon handler a principal gets no files or memory leg.
//
// SPORT: cmd/cascade/daemon (CHANGED, recall.what conversation leg).
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
)

var recallWhatTestClock = runtime.NewFixedClock(time.Unix(1_700_000_000, 0))

// recallWhatTestStore opens a migrated sqlite conversation store whose
// thread_scope table exists (created by the exported owner view path).
func recallWhatTestStore(t *testing.T) conversation.Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := conversation.ApplyConversationSchema(ctx, db, migrate.SQLiteEmitter{}, recallWhatTestClock, dbPath, filepath.Join(dir, "backups")); err != nil {
		t.Fatalf("ApplyConversationSchema: %v", err)
	}
	store := conversation.NewStore(db)
	owner := principal.WithCaller(ctx, principal.Caller{OwnerCLI: true})
	if _, err := conversation.CallerThreadView(owner, store, recallWhatTestClock, "project:seed"); err != nil {
		t.Fatalf("thread scope schema: %v", err)
	}
	return store
}

// seedRecallThread stores one turn on thread id in project, attributed to
// attribution ("" writes no attribution row), at tier; the content repeats
// "orchid" n times so a larger n ranks higher.
func seedRecallThread(t *testing.T, s conversation.Store, id, project, attribution string, tier provider.SensitivityTier, n int) string {
	t.Helper()
	ctx := context.Background()
	turn := conversation.Turn{ID: conversation.NewTurnID(id, 0, conversation.RoleUser), ThreadID: id, Role: conversation.RoleUser, CreatedAt: 5}
	seg := conversation.Segment{ID: conversation.NewSegmentID(turn.ID, 0, conversation.SegmentText), TurnID: turn.ID,
		Kind: conversation.SegmentText, Content: strings.Repeat("orchid ", n) + "tail", CreatedAt: 5}
	if attribution == "" {
		if err := s.AppendFirstTurnProject(ctx, tier, turn, []conversation.Segment{seg}, &project); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		return "conversation:" + turn.ID
	}
	if err := s.CreateThreadProject(ctx, id, tier, attribution, &project, 5); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
	if err := s.AppendTurn(ctx, turn); err != nil {
		t.Fatalf("append %s: %v", id, err)
	}
	if err := s.AppendSegment(ctx, seg); err != nil {
		t.Fatalf("segment %s: %v", id, err)
	}
	return "conversation:" + turn.ID
}

// realStoreService builds recall.what over only the real conversation leg,
// resolved to project, with the process egress engine.
func realStoreService(t *testing.T, s conversation.Store, project string) *retrieval.RecallWhatService {
	t.Helper()
	bindTestProcessFirewall(t)
	engine, err := egress.ProcessEngine(egress.DefaultRegistry())
	if err != nil {
		t.Fatalf("ProcessEngine: %v", err)
	}
	resolver := resumeQueryTestScopeResolver{out: scope.SessionScope{Kind: scope.ScopeKindSession, Project: project}}
	svc, err := retrieval.NewRecallWhatService(nil, conversationLegAdapter{store: s}, nil, rrf.Params{}, recallWhatTestClock, resolver, engine)
	if err != nil {
		t.Fatalf("NewRecallWhatService: %v", err)
	}
	return svc
}

// recallIDs runs one query and returns the result ids in rank order.
func recallIDs(ctx context.Context, t *testing.T, svc *retrieval.RecallWhatService, k int) []string {
	t.Helper()
	resp, err := svc.Query(ctx, retrieval.RecallWhatRequest{Query: "orchid", K: k})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	ids := make([]string, 0, len(resp.Results))
	for _, r := range resp.Results {
		ids = append(ids, r.ID)
	}
	return ids
}

// TestConversationLeg_ScopeEnforcedRealStore: over the real SearchTurns,
// project B and no-project threads outrank project A's, and k is 1; a
// caller resolved to A gets exactly A's turn.
func TestConversationLeg_ScopeEnforcedRealStore(t *testing.T) {
	s := recallWhatTestStore(t)
	seedRecallThread(t, s, "th-b", "repoB", "owner", provider.SensitivityPublic, 6)
	ctx := context.Background()
	turn := conversation.Turn{ID: conversation.NewTurnID("th-none", 0, conversation.RoleUser), ThreadID: "th-none", Role: conversation.RoleUser, CreatedAt: 5}
	seg := conversation.Segment{ID: conversation.NewSegmentID(turn.ID, 0, conversation.SegmentText), TurnID: turn.ID,
		Kind: conversation.SegmentText, Content: strings.Repeat("orchid ", 5), CreatedAt: 5}
	if err := s.AppendFirstTurnProject(ctx, provider.SensitivityPublic, turn, []conversation.Segment{seg}, nil); err != nil {
		t.Fatalf("seed th-none: %v", err)
	}
	own := seedRecallThread(t, s, "th-a", "repoA", "owner", provider.SensitivityPublic, 1)
	owner := principal.WithCaller(ctx, principal.Caller{OwnerCLI: true})
	if got := recallIDs(owner, t, realStoreService(t, s, "repoA"), 1); len(got) != 1 || got[0] != own {
		t.Fatalf("ids = %v, want only %s", got, own)
	}
}

// TestConversationLegPrincipalSeesOnlyAttributed: in the principal's own
// project, owner-attributed, unattributed and foreign-principal threads all
// outrank its own thread, and its own scope also attributes a thread in
// another project; with k=1 the principal gets exactly its own thread, while
// the owner sees every thread of the project and none of the other.
func TestConversationLegPrincipalSeesOnlyAttributed(t *testing.T) {
	s := recallWhatTestStore(t)
	const proj = "project:p"
	ownerRow := seedRecallThread(t, s, "th-owner", proj, "owner", provider.SensitivityPublic, 5)
	unattr := seedRecallThread(t, s, "th-unattr", proj, "", provider.SensitivityPublic, 4)
	foreign := seedRecallThread(t, s, "th-foreign", proj, "project:f", provider.SensitivityPublic, 3)
	elsewhere := seedRecallThread(t, s, "th-elsewhere", "project:q", proj, provider.SensitivityPublic, 2)
	own := seedRecallThread(t, s, "th-own", proj, proj, provider.SensitivityPublic, 1)
	svc := realStoreService(t, s, proj)
	p := principal.Principal{ID: "p1", Kind: principal.KindSession, Scope: proj}
	asPrincipal := principal.WithCaller(context.Background(), principal.Caller{Principal: &p})
	if got := recallIDs(asPrincipal, t, svc, 1); len(got) != 1 || got[0] != own {
		t.Fatalf("principal ids = %v, want only its own %s", got, own)
	}
	if got := recallIDs(asPrincipal, t, svc, 10); len(got) != 1 || got[0] != own {
		t.Fatalf("principal ids (k=10) = %v, want only its own %s", got, own)
	}
	owner := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	got := strings.Join(recallIDs(owner, t, svc, 10), ",")
	for _, id := range []string{ownerRow, unattr, foreign, own} {
		if !strings.Contains(got, id) {
			t.Fatalf("owner ids = %s, missing %s (the fixture is not vacuous)", got, id)
		}
	}
	if strings.Contains(got, elsewhere) {
		t.Fatalf("owner ids = %s include another project's thread", got)
	}
}

// TestConversationLeg_RealEngineRefusesRestrictedAndLocalOnly: one phrase
// on local-only, restricted, internal and public threads of the caller's
// project; through the process engine only internal and public return.
func TestConversationLeg_RealEngineRefusesRestrictedAndLocalOnly(t *testing.T) {
	s := recallWhatTestStore(t)
	seedRecallThread(t, s, "th-local", "repoA", "", provider.SensitivityLocalOnly, 4)
	seedRecallThread(t, s, "th-restricted", "repoA", "", provider.SensitivityRestricted, 3)
	internal := seedRecallThread(t, s, "th-internal", "repoA", "", provider.SensitivityInternal, 2)
	public := seedRecallThread(t, s, "th-public", "repoA", "", provider.SensitivityPublic, 1)
	owner := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	got := recallIDs(owner, t, realStoreService(t, s, "repoA"), 10)
	if strings.Join(got, ",") != internal+","+public {
		t.Fatalf("ids = %v, want exactly [%s %s]", got, internal, public)
	}
}

// TestRecallWhatPrincipalGetsNoOwnerFilesOrMemory: over the composed daemon
// handler registered with a principal access policy, a minted principal
// token gets the files and memory legs refused ("not in principal scope")
// and only the conversation leg; the owner gets all three legs; a caller
// with no identity is refused before the handler.
func TestRecallWhatPrincipalGetsNoOwnerFilesOrMemory(t *testing.T) {
	paths := firewallTestPaths(t)
	resetProcessFirewall(t)
	selectProcessCustody = testProcessCustody(t.TempDir())
	store := storetest.NewMemStore()
	h, err := buildRecallWhatHandler(paths, recallWhatTestClock, events.New(store, recallWhatTestClock), store)
	if err != nil {
		t.Fatalf("buildRecallWhatHandler: %v", err)
	}
	cwd := bindRecallWhatRepo(t, paths)
	registry := rpc.NewRegistry()
	h.Register(registry)
	minter := principal.NewMinter(recallWhatTestClock)
	token, err := minter.Mint(principal.Principal{ID: "p1", Kind: principal.KindSession, Scope: "project:p"}, time.Hour)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	registry.SetAccessPolicy(func(tok string) (principal.Principal, error) { return minter.Resolve(tok, recallWhatTestClock.Now()) })
	p, err := minter.Resolve(token, recallWhatTestClock.Now())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	asPrincipal := principal.WithCaller(context.Background(), principal.Caller{Principal: &p})
	resp := dispatchRecallWhat(asPrincipal, t, registry, cwd)
	for _, d := range []string{retrieval.DomainFile, retrieval.DomainMemory} {
		if resp.Errors[d] != errNotInPrincipalScope.Error() || !strings.HasSuffix(resp.Errors[d], "not in principal scope") {
			t.Fatalf("principal Errors[%s] = %q, want %q", d, resp.Errors[d], errNotInPrincipalScope.Error())
		}
	}
	for _, r := range resp.Results {
		if r.Domain != retrieval.DomainConversation {
			t.Fatalf("principal received a %s row: %+v", r.Domain, r)
		}
	}
	if strings.Join(resp.Legs, ",") != retrieval.DomainConversation {
		t.Fatalf("principal legs = %v, want only conversation", resp.Legs)
	}
	ownerResp := dispatchRecallWhat(principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true}), t, registry, cwd)
	if got := strings.Join(ownerResp.Legs, ","); got != "conversation,file,memory" || len(ownerResp.Errors) != 0 {
		t.Fatalf("owner legs = %q errors = %v, want all three legs and no errors", got, ownerResp.Errors)
	}
	if _, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: retrieval.MethodWhat, Params: json.RawMessage(`{"query":"orchid"}`)}); errObj == nil {
		t.Fatal("a caller with no identity reached recall.what")
	}
}

// bindRecallWhatRepo binds a fresh working directory to a repository in
// the daemon's scope graph, so the real resolver answers a project scope.
func bindRecallWhatRepo(t *testing.T, paths runtime.PathProvider) string {
	t.Helper()
	cwd := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(paths.DataDir(), "cascade.db")+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	g := scope.NewGraphStore(db)
	ctx := context.Background()
	if err := g.PutRepository(ctx, scope.RepositoryRecord{ID: "repo-own"}); err != nil {
		t.Fatalf("PutRepository: %v", err)
	}
	if err := g.PutRepoPath(ctx, scope.RepoPathRecord{RootPath: cliGitRoot(ctx, cwd), RepositoryID: "repo-own"}); err != nil {
		t.Fatalf("PutRepoPath: %v", err)
	}
	raw, _ := json.Marshal(recall.CatalogDoc{Version: recall.CatalogVersion})
	if err := os.MkdirAll(recallIndexDir(paths), 0o700); err != nil {
		t.Fatalf("index dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(recallIndexDir(paths), recall.CatalogFileName), raw, 0o600); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	return cwd
}

// dispatchRecallWhat runs recall.what from cwd through registry as ctx's caller.
func dispatchRecallWhat(ctx context.Context, t *testing.T, registry *rpc.Registry, cwd string) retrieval.RecallWhatResponse {
	t.Helper()
	params, _ := json.Marshal(retrieval.WhatParams{Query: "orchid", Cwd: cwd})
	res, errObj := registry.Dispatch(ctx, &rpc.Request{Method: retrieval.MethodWhat, Params: params})
	if errObj != nil {
		t.Fatalf("Dispatch: %+v", errObj)
	}
	resp, ok := res.(retrieval.RecallWhatResponse)
	if !ok {
		t.Fatalf("result type %T", res)
	}
	return resp
}
