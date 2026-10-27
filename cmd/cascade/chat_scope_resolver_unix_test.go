//go:build !windows

// Purpose: the chat scope resolver (chat_scope_resolver_unix.go) names the same
// project recall.what narrows by: a real recall.what service over the same
// resolver accepts the chat ref as the caller's scope claim and refuses any
// other, for a registered project and for the general bucket. A resolver that
// cannot be built or fails leaves the thread unscoped (an error, not "").
// Constraints: CASCADE_HOME is a t.TempDir(), the scope graph is seeded in it
// with obviously fake repository records, and nothing touches the real HOME.
// SPORT: cmd/cascade chat scope resolver (TEST) -- P1-CPA-06.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

const fakeRepoID = "repo-fake-chat-scope"

// chatScopePaths is a path provider rooted in a temp CASCADE_HOME.
func chatScopePaths(t *testing.T) runtime.PathProvider {
	t.Helper()
	home := t.TempDir()
	paths, err := runtime.NewPathProvider(func(k string) string {
		if k == "CASCADE_HOME" {
			return home
		}
		return ""
	}, func() (string, error) { return home, nil })
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

// seedFakeProject registers a fake repository whose root is root in the
// scope graph of paths' cascade.db.
func seedFakeProject(t *testing.T, paths runtime.PathProvider, root string) {
	t.Helper()
	db, err := openSecondaryCascadeDB(paths, "chat scope test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	seedFakeProjectDB(t, paths, db, root)
}

func seedFakeProjectDB(t *testing.T, paths runtime.PathProvider, db *sql.DB, root string) {
	t.Helper()
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	if err := scope.ApplyScopeSchema(context.Background(), db, migrate.SQLiteEmitter{}, runtime.NewSystemClock(), dbPath, filepath.Join(paths.DataDir(), "backups")); err != nil {
		t.Fatal(err)
	}
	store := scope.NewGraphStore(db)
	rec := scope.RepositoryRecord{ID: fakeRepoID, RootPath: root, Remote: "https://example.invalid/fake.git"}
	if err := store.PutRepository(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoPath(context.Background(), scope.RepoPathRecord{RootPath: root, RepositoryID: fakeRepoID}); err != nil {
		t.Fatal(err)
	}
}

// recallAccepts runs a real recall.what service over the real resolver with
// claim as the caller's scope and reports whether it was accepted as the
// resolved scope for cwd.
func recallAccepts(t *testing.T, paths runtime.PathProvider, cwd, claim string) bool {
	t.Helper()
	resolver, err := recallWhatScopeResolver(paths)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := retrieval.NewRecallWhatService(fakeWhatFilesLeg{}, nil, nil, rrf.Params{}, nil, resolver, testCLIEgress{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Query(context.Background(), retrieval.RecallWhatRequest{
		Query: "anything", ResolveIn: scope.ResolveInput{Cwd: cwd}, Scope: claim})
	if err != nil && !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("recall.what Query: %v", err)
	}
	return err == nil
}

// TestChatScopeResolverMatchesRecallWhat: for a cwd inside a registered
// project and for one outside any, the chat ref is exactly the scope claim
// recall.what accepts, and a different claim is refused by recall.what.
// Failing input: a chat mapping that returns the Kind, the cwd or the repo
// root instead of the resolved project.
func TestChatScopeResolverMatchesRecallWhat(t *testing.T) {
	paths := chatScopePaths(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seedFakeProject(t, paths, root)
	general, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resolve := chatScopeResolver(paths)
	for name, tc := range map[string]struct{ cwd, want string }{
		"registered project": {root, fakeRepoID}, "general": {general, ""},
	} {
		got, err := resolve(context.Background(), scope.ResolveInput{Cwd: tc.cwd})
		if err != nil || got != tc.want {
			t.Fatalf("%s: chat ref = %q, %v; want %q", name, got, err, tc.want)
		}
		if !recallAccepts(t, paths, tc.cwd, got) {
			t.Fatalf("%s: recall.what refused the chat ref %q as the resolved scope", name, got)
		}
		if recallAccepts(t, paths, tc.cwd, "not-"+fakeRepoID) {
			t.Fatalf("%s: recall.what accepted a foreign claim; the comparison is vacuous", name)
		}
	}
}

// TestChatProjectRefRefusesProjectlessScope: only ScopeKindGeneral is the
// general bucket. A resolved scope with no project is an error (unscoped),
// so "" never silently stands for anything else.
func TestChatProjectRefRefusesProjectlessScope(t *testing.T) {
	if ref, err := chatProjectRef(scope.SessionScope{Kind: scope.ScopeKindGeneral}); err != nil || ref != "" {
		t.Fatalf("general = %q, %v; want \"\", nil", ref, err)
	}
	if ref, err := chatProjectRef(scope.SessionScope{Kind: scope.ScopeKindSession}); err == nil || ref != "" {
		t.Fatalf("session with no project = %q, %v; want an error", ref, err)
	}
}

// TestChatScopeResolverBuildFailureIsAnError: a data dir that cannot be
// created makes the resolver error on every call (thread unscoped), never
// return the general bucket.
func TestChatScopeResolverBuildFailureIsAnError(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths, err := runtime.NewPathProvider(func(k string) string {
		if k == "CASCADE_HOME" {
			return filepath.Join(blocker, "home")
		}
		return ""
	}, func() (string, error) { return blocker, nil })
	if err != nil {
		t.Fatal(err)
	}
	if ref, err := chatScopeResolver(paths)(context.Background(), scope.ResolveInput{Cwd: "/fake/dir"}); err == nil || ref != "" {
		t.Fatalf("resolver over an unusable data dir = %q, %v; want an error", ref, err)
	}
}

// TestChatWiring_RecordsOwnerThreadProject wires the chat handlers exactly as
// the daemon does (wireChatHandlers) and sends an OWNER chat.create_thread
// whose cwd is in no registered project. The wired resolver must have run: the
// thread then has a general-bucket row "". Failing input: wireChatHandlers not
// calling SetProjectResolver leaves the thread with no row.
func TestChatWiring_RecordsOwnerThreadProject(t *testing.T) {
	clock := runtime.NewSystemClock()
	home := shortCascadeHome(t)
	registry := rpc.NewRegistry()
	bus := events.New(storetest.NewMemStore(), clock)
	if err := wireChatHandlers(context.Background(), registry, fakeDaemonPaths{root: home}, clock, bus,
		testChatCustody(t, nil), storetest.NewMemStore()); err != nil {
		t.Fatalf("wireChatHandlers: %v", err)
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"chat.create_thread","params":{"privacy_mode":"internal","cwd":"/fake/nonexistent/project-dir"}}`
	req, perr := rpc.Parse([]byte(body))
	if perr != nil {
		t.Fatalf("parse: %+v", perr)
	}
	ctx := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	res, eo := registry.Dispatch(ctx, req)
	if eo != nil {
		t.Fatalf("chat.create_thread: %+v", eo)
	}
	raw, _ := json.Marshal(res)
	var created struct {
		ThreadID string `json:"thread_id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.ThreadID == "" {
		t.Fatalf("create_thread result %s: %v", raw, err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(home, "data", "cascade.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var ref string
	err = db.QueryRow(`SELECT project_ref FROM conversation_thread_project WHERE thread_id = ?`, created.ThreadID).Scan(&ref)
	if err != nil || ref != "" {
		t.Fatalf("project row for %s = %q, %v; want the general bucket row \"\"", created.ThreadID, ref, err)
	}
}
