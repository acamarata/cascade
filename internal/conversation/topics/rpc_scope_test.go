// Package topics (rpc_scope_test.go): Purpose: prove the topic verbs' scope
//   filter both ways for a principal and the owner.
// Inputs: a real sqlite conversation store opened under t.TempDir().
// Outputs: n/a (test file).
// Constraints: no path literals; the db is closed by t.Cleanup.
// SPORT: internal/conversation/topics rpc scope (TEST).

package topics

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// newScopedRPCFixture returns handlers over a store whose db the test can reach.
func newScopedRPCFixture(t *testing.T) (*RPCHandlers, conversation.Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "topics-scope-test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	clock := newFixedClock()
	if err := conversation.ApplyConversationSchema(context.Background(), db, migrate.SQLiteEmitter{}, migrate.Clock(clock), "", ""); err != nil {
		t.Fatalf("ApplyConversationSchema: %v", err)
	}
	store := conversation.NewStore(db)
	h, err := NewRPCHandlers(store, clock)
	if err != nil {
		t.Fatalf("NewRPCHandlers: %v", err)
	}
	return h, store, db
}

func isDenied(err error) bool {
	k, ok := cascade.KindOf(err)
	return ok && k == cascade.KindPermissionDenied
}

func TestTopicVerbsFilterByCallerScope(t *testing.T) {
	h, store, db := newScopedRPCFixture(t)
	alpha := seedTopicThread(t, store, "alpha", 1)
	seedTopicThread(t, store, "beta", 1)
	pctx := principal.WithCaller(context.Background(), principal.Caller{Principal: &principal.Principal{ID: "p-a", Kind: principal.KindSession, Scope: "project:a"}})
	octx := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	if _, err := conversation.CallerThreadView(pctx, store, newFixedClock(), ""); err != nil {
		t.Fatalf("CallerThreadView: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO conversation_thread_scope (thread_id, scope_ref) VALUES (?, 'project:a')`, string(alpha)); err != nil {
		t.Fatalf("attribute alpha: %v", err)
	}
	threads := func(ctx context.Context) threadsListResult {
		res, err := h.handleThreadsList(ctx, nil)
		if err != nil {
			t.Fatalf("handleThreadsList: %v", err)
		}
		return res.(threadsListResult)
	}
	topics := func(ctx context.Context) []string {
		res, err := h.handleTopicsList(ctx, nil)
		if err != nil {
			t.Fatalf("handleTopicsList: %v", err)
		}
		var out []string
		for _, s := range res.(topicsListResult).Topics {
			out = append(out, s.TopicType)
		}
		return out
	}

	pt := threads(pctx)
	if pt.TotalCount != 1 || len(pt.Threads) != 1 || pt.Threads[0].ID != string(alpha) {
		t.Fatalf("principal threads_list = %+v, want only %s", pt, alpha)
	}
	if got := topics(pctx); len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("principal topics_list = %v, want [alpha]", got)
	}
	if ot := threads(octx); ot.TotalCount != 2 {
		t.Fatalf("owner threads_list total = %d, want 2", ot.TotalCount)
	}
	if got := topics(octx); len(got) != 2 {
		t.Fatalf("owner topics_list = %v, want alpha and beta", got)
	}
	other, _ := json.Marshal(map[string]string{"scope": "project:b"})
	if _, err := h.handleThreadsList(pctx, other); !isDenied(err) {
		t.Fatalf("threads_list foreign scope err = %v, want permission denied", err)
	}
	if _, err := h.handleTopicsList(pctx, other); !isDenied(err) {
		t.Fatalf("topics_list foreign scope err = %v, want permission denied", err)
	}
}
