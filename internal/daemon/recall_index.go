// Purpose: registers the recall.index.rebuild|verify|migrate|update
// namespace (F/S-11.T4) on the daemon's RPC router, over a real
// internal/retrieval/lifecycle.Manager, and supplies the real git tree
// generation marker (R-21.189) and git-diff functions that
// package deliberately never imports os/exec to compute itself.
//
// Inputs: the daemon's shared *rpc.Registry, its runtime.PathProvider and
// runtime.Clock (buildRPCServer already threads all three into every
// sibling registerXHandler), and the already-open provider.Store
// (cmd/cascade/daemon_unix_store.go's openRuntimeStore) the FTS5 leg and
// this package's own manifest/marker bookkeeping read and write.
//
// Outputs: internal/retrieval/lifecycle's four RPC methods, bound to a
// real Manager over the real store; migrate additionally opens its own
// second sqlite connection to apply the retrieval domain's MigrationSet,
// mirroring context_scope.go's documented "second connection" choice.
//
// Constraints: os/exec lives ONLY in this file's two GitTreeHashFunc/
// GitDiffFunc implementations — internal/daemon is already an
// EgressExecNotYetMigrated os/exec importer (context_scope.go's
// gitRootExec doc comment), so this adds no new unlisted importer to the
// A-T2 arch gate. A nil store (a test's minimal buildRPCServer call)
// disables this namespace entirely rather than registering a handler
// that would panic on first use — the same degradation
// registerMemoryHandler already applies to a nil memoryAdmin.
//
// SPORT: internal/daemon (CHANGED — recall.index.* registration,
// P1-E06-W2-S11-T4).
package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/lifecycle"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/localvector"
)

// RecallIndexRebuildMethod, RecallIndexVerifyMethod, RecallIndexMigrateMethod
// and RecallIndexUpdateMethod are the recall.index.* JSON-RPC method names
// (07-CLI-COMMAND-TREE §recall).
const (
	RecallIndexRebuildMethod = "recall.index.rebuild"
	RecallIndexVerifyMethod  = "recall.index.verify"
	RecallIndexMigrateMethod = "recall.index.migrate"
	RecallIndexUpdateMethod  = "recall.index.update"
)

// RegisterRecallIndexHandler mounts the recall.index.* namespace on
// registry, over a Manager built against store. A nil store leaves the
// namespace unregistered: a build with no runtime store configured (a
// minimal test harness) is not this ticket's problem to solve, and an
// unknown-method response is the correct, honest answer for it — the
// same choice registerMemoryHandler's nil-admin branch already makes.
func RegisterRecallIndexHandler(
	registry *rpc.Registry, paths runtime.PathProvider, clock runtime.Clock, store provider.Store, dbPath string,
) error {
	if store == nil {
		return nil
	}
	manager, err := lifecycle.NewManager(lifecycle.ManagerOptions{
		CatalogPath: filepath.Join(recallIndexDataDir(paths), recallIndexCatalogName),
		Store:       store,
		Index:       mustIndex(store),
		Vectors:     localvector.New(store),
		Clock:       clock,
		TreeHash:    gitTreeHashExec,
	})
	if err != nil {
		return err
	}
	registry.Register(RecallIndexRebuildMethod, recallIndexRebuildHandler(manager))
	registry.Register(RecallIndexVerifyMethod, recallIndexVerifyHandler(manager))
	registry.Register(RecallIndexUpdateMethod, recallIndexUpdateHandler(manager))
	registry.Register(RecallIndexMigrateMethod, recallIndexMigrateHandler(clock, dbPath))
	return nil
}

// recallIndexCatalogName is the catalog document's file name within
// recallIndexDataDir(paths), matching recall.CatalogFileName
// (cmd/cascade/daemon_unix_handlers.go's recallIndexDir builds the same
// {DataDir}/retrieval directory; that helper is unexported to package
// main, so this file computes the same path independently rather than
// reaching across the cmd/internal boundary).
const recallIndexCatalogName = "catalog.json"

// recallIndexDataDir is {CASCADE_HOME}/data/retrieval, matching
// cmd/cascade/daemon_unix_handlers.go's recallIndexDir exactly.
func recallIndexDataDir(paths runtime.PathProvider) string {
	return filepath.Join(paths.DataDir(), "retrieval")
}

// mustIndex builds the FTS5 leg over store. retrieval.NewIndex only
// errors on a nil store, which this call site never passes.
func mustIndex(store provider.Store) *retrieval.Index {
	idx, _ := retrieval.NewIndex(store)
	return idx
}

// recallIndexRebuildHandler serves recall.index.rebuild.
func recallIndexRebuildHandler(m *lifecycle.Manager) rpc.HandlerFunc {
	return func(ctx context.Context, _ json.RawMessage) (any, error) { return m.Rebuild(ctx) }
}

// recallIndexVerifyHandler serves recall.index.verify.
func recallIndexVerifyHandler(m *lifecycle.Manager) rpc.HandlerFunc {
	return func(ctx context.Context, _ json.RawMessage) (any, error) { return m.Verify(ctx) }
}

// recallIndexUpdateHandler serves recall.index.update.
func recallIndexUpdateHandler(m *lifecycle.Manager) rpc.HandlerFunc {
	return func(ctx context.Context, _ json.RawMessage) (any, error) { return m.Update(ctx, gitDiffExec) }
}

// recallIndexMigrateHandler serves recall.index.migrate, opening its own
// second sqlite connection to dbPath for the duration of one call —
// context_scope.go's documented tradeoff, applied here for the same
// reason: threading a *sql.DB into buildRPCServer's signature would touch
// every existing call site and test, none of which are this ticket's.
func recallIndexMigrateHandler(clock runtime.Clock, dbPath string) rpc.HandlerFunc {
	return func(ctx context.Context, _ json.RawMessage) (any, error) {
		db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
		if err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "recall.index.migrate: open cascade.db")
		}
		defer func() { _ = db.Close() }()
		return lifecycle.Migrate(ctx, lifecycle.MigrateDeps{
			DB: db, Dialect: migrate.SQLiteEmitter{}, Clock: clock,
			DBPath: dbPath, BackupDir: filepath.Join(filepath.Dir(dbPath), "backups"),
		})
	}
}

// gitTreeHashExec is the production lifecycle.GitTreeHashFunc: the
// current commit plus a stable digest of the working tree's uncommitted
// changes, so an edit changes the marker even before it is committed.
// Falls back to the empty string on any git failure (no repository, git
// absent) rather than erroring — an install with no git repository is a
// supported configuration (the marker then always reads DRIFTED, which
// is the documented fail-closed behavior for an unresolvable marker).
func gitTreeHashExec(ctx context.Context) (string, error) {
	head, err := recallIndexRunGit(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", nil //nolint:nilerr // no repository is a supported, not an error, configuration
	}
	status, err := recallIndexRunGit(ctx, "status", "--porcelain")
	if err != nil {
		status = ""
	}
	return head + ":" + retrieval.ChunkID([]byte(status)), nil
}

// gitDiffExec is the production lifecycle.GitDiffFunc: the paths that
// changed between sinceTreeHash's commit and the current working tree.
// sinceTreeHash carries gitTreeHashExec's own "<commit>:<digest>" shape;
// only the commit half is meaningful to `git diff`.
func gitDiffExec(ctx context.Context, sinceTreeHash string) ([]lifecycle.ChangedPath, string, error) {
	commit, _, _ := strings.Cut(sinceTreeHash, ":")
	newTree, err := gitTreeHashExec(ctx)
	if err != nil || commit == "" {
		return nil, newTree, err
	}
	out, err := recallIndexRunGit(ctx, "diff", "--name-status", commit, "--")
	if err != nil {
		return nil, newTree, nil //nolint:nilerr // a diff against an unknown commit is treated as "nothing to report"
	}
	return parseNameStatus(out), newTree, nil
}

// parseNameStatus turns `git diff --name-status` output into ChangedPaths.
func parseNameStatus(out string) []lifecycle.ChangedPath {
	var changed []lifecycle.ChangedPath
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		changed = append(changed, lifecycle.ChangedPath{Path: fields[len(fields)-1], Deleted: fields[0] == "D"})
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i].Path < changed[j].Path })
	return changed
}

// recallIndexRunGit runs `git <args>` in the current working directory, returning
// stdout with surrounding whitespace trimmed.
func recallIndexRunGit(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if wd, err := os.Getwd(); err == nil {
		cmd.Dir = wd
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
