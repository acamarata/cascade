package daemon

// Purpose: registers context.scope.show (E/S-08.T4) on the daemon's RPC
//   router, split out of daemon.go under its 300-line cap (the same split
//   cmd/cascade/daemon_unix_handlers.go already applies to buildRPCServer's
//   registrations, for the same reason: keep the file that owns the shared
//   ceiling small and give each registering ticket its own file).
//
// Inputs: the daemon's shared *rpc.Registry, its runtime.PathProvider, and
//   its runtime.Clock (buildRPCServer already threads all three into every
//   sibling registerXHandler it calls -- see daemon_unix_handlers.go).
//
// Outputs: internal/context/scope's context.scope.show handler, bound to a
//   real modernc SQLite connection under paths.DataDir()/cascade.db and a
//   real `git rev-parse --show-toplevel` GitRootFunc.
//
// Constraints: opens its OWN *sql.DB handle to cascade.db rather than
//   reusing platformDaemonRun's rawDB (openRuntimeStore's capture,
//   cmd/cascade/daemon_unix_store.go): threading rawDB into this
//   registration would require adding a context.Context and *sql.DB
//   parameter to buildRPCServer's exported signature, which every one of
//   its existing call sites and tests (cmd/cascade/daemon_unix.go,
//   daemon_unix_run_test.go, daemon_unix_reload.go) would then need to
//   follow -- none of those files are in this ticket's files_scope, and
//   none of them are locked to this lane. A second sqlite/database-sql
//   connection to the same file, opened with the same busy_timeout the
//   CLI's own embedded path (cmd/cascade/context_scope.go) already uses,
//   is the documented tradeoff: SQLite supports concurrent connections to
//   one file, and ApplyScopeSchema is idempotent by contract, so a second
//   connection applying the same schema is a no-op after the first. This
//   mirrors registerRecallHandler's own choice to own its file-backed
//   catalog rather than reuse rawDB.
//
// SPORT: internal/daemon (CHANGED, context.scope.show registration,
//   E/S-08.T4).
import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// RegisterContextScopeHandler opens the scope graph's SQLite connection
// under paths.DataDir(), applies the MigrationSet (idempotent), and
// registers ContextScopeMethod against registry. Returns the opened *sql.DB
// so the caller can close it during daemon shutdown; a non-nil error means
// no db was left open (this function closes its own db before returning
// an error).
func RegisterContextScopeHandler(registry *rpc.Registry, paths runtime.PathProvider, clock runtime.Clock) (*sql.DB, error) {
	// DataDir does not always pre-exist when this runs first: several
	// existing buildRPCServer fixtures (e.g. cmd/cascade's
	// fakeMemoryPaths{root: t.TempDir()}) point DataDir() at a "data"
	// subdirectory t.TempDir() itself never creates, matching
	// daemon_unix_store.go's openRuntimeStore, which makes the same call
	// for the same reason before opening its own cascade.db handle.
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "daemon: context.scope.show: create data dir")
	}
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "daemon: context.scope.show: open cascade.db")
	}

	backupDir := filepath.Join(paths.DataDir(), "backups")
	if err := scope.ApplyScopeSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, dbPath, backupDir); err != nil {
		_ = db.Close()
		return nil, err
	}

	deps := scope.ResolveDeps{Store: scope.NewGraphStore(db), GitRoot: gitRootExec}
	registry.Register(ContextScopeMethod, func(ctx context.Context, raw json.RawMessage) (any, error) {
		return scope.ContextScopeShow(ctx, deps, raw)
	})
	return db, nil
}

// gitRootExec is the production scope.GitRootFunc for the daemon side of
// context.scope.show: `git rev-parse --show-toplevel`, falling back to cwd
// on any failure (git absent, not a repository, subprocess error) -- never
// an error, matching cmd/cascade/context_scope.go's cliGitRoot exactly.
// internal/daemon is already an EgressExecNotYetMigrated os/exec importer
// (internal/build/egress_allow.go: "starts and stops the daemon process"),
// so this call site adds no new unlisted importer to the A-T2 arch gate.
func gitRootExec(ctx context.Context, cwd string) string {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return cwd
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return cwd
	}
	return filepath.Clean(filepath.FromSlash(root))
}
