package daemon

// Purpose: the recall.index.* dispatch test, split from
//   recall_index_test.go which reached the 300-line cap. Kept as its own
//   file rather than trimmed, because the two halves test different
//   things: the other file covers the pure helpers and the git seam, this
//   one drives the registered handlers through the real registry.
// Constraints: Art.2 -- a real git repository and a real modernc SQLite
//   store under t.TempDir(); no doubles. Art.7.1 -- nothing written
//   outside t.TempDir(), no network listener.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/providers/sqlite"
)

// TestRegisterRecallIndexHandler_AllFourMethodsDispatch drives every verb
// through the REAL registry.Dispatch entry point against a real modernc
// SQLite store. It asserts BOTH states that matter: before any index
// exists, verify and update must refuse with actionable typed errors
// rather than reporting a healthy empty index, and after a rebuild all
// four must succeed. A handler registered but broken would pass a mere
// Registered() check, which is why this dispatches.
func TestRegisterRecallIndexHandler_AllFourMethodsDispatch(t *testing.T) {
	dir := t.TempDir()
	seedRepo(t, dir)
	chdir(t, dir)

	dbPath := filepath.Join(dir, "cascade.db")
	store, err := sqlite.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("opening the real SQLite database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	reg := rpc.NewRegistry()
	paths := fakePaths{root: dir}
	if err := os.MkdirAll(recallIndexDataDir(paths), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RegisterRecallIndexHandler(reg, paths, runtime.NewSystemClock(), store, dbPath); err != nil {
		t.Fatalf("RegisterRecallIndexHandler: %v", err)
	}

	for _, m := range []string{
		RecallIndexRebuildMethod, RecallIndexVerifyMethod,
		RecallIndexUpdateMethod, RecallIndexMigrateMethod,
	} {
		if !reg.Registered(m) {
			t.Fatalf("method %q is not registered", m)
		}
	}

	dispatch := func(t *testing.T, method string) (any, *rpc.ErrorObject) {
		t.Helper()
		return reg.Dispatch(context.Background(), &rpc.Request{
			JSONRPC: "2.0", Method: method, ID: []byte("1"),
		})
	}

	assertRefusesBeforeIndex(t, dispatch)
	assertSucceedsAfterRebuild(t, dispatch)
}

type dispatchFunc func(*testing.T, string) (any, *rpc.ErrorObject)

// assertRefusesBeforeIndex pins the pre-rebuild contract: refuse with
// guidance rather than report a healthy empty index. A silent success here
// would tell a user their retrieval is fine when nothing is indexed at all.
func assertRefusesBeforeIndex(t *testing.T, dispatch dispatchFunc) {
	t.Helper()
	t.Run("verify refuses before any index exists", func(t *testing.T) {
		if _, errObj := dispatch(t, RecallIndexVerifyMethod); errObj == nil {
			t.Fatal("verify on a never-built index succeeded; want a typed refusal")
		}
	})
	t.Run("update refuses without a generation marker", func(t *testing.T) {
		if _, errObj := dispatch(t, RecallIndexUpdateMethod); errObj == nil {
			t.Fatal("update with no generation marker succeeded; want a typed refusal")
		}
	})
}

// assertSucceedsAfterRebuild runs migrate then rebuild, which establishes
// the index, then re-runs verify and update now that both have something
// to work against.
func assertSucceedsAfterRebuild(t *testing.T, dispatch dispatchFunc) {
	t.Helper()
	for _, m := range []string{
		RecallIndexMigrateMethod, RecallIndexRebuildMethod,
		RecallIndexVerifyMethod, RecallIndexUpdateMethod,
	} {
		t.Run("after rebuild "+m, func(t *testing.T) {
			res, errObj := dispatch(t, m)
			if errObj != nil {
				t.Fatalf("dispatch %s returned a transport error: %+v", m, errObj)
			}
			if res == nil {
				t.Fatalf("dispatch %s returned a nil result", m)
			}
		})
	}
}
