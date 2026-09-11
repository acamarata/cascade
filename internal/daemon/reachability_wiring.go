package daemon

// Purpose: calls Manifest.RegisterReachability for real at daemon startup
//   (R-16.80 Ruling 2, the identical treatment RegisterConductorRouter
//   gets in conductor_execute.go): before this file, nothing under
//   internal/daemon (or anywhere else) called it in production.
// Inputs: paths.DataDir()/cascade.db (the same file context.scope.show
//   opens its own connection to - ApplyGraphSchema's own doc comment
//   requires internal/context/scope's ApplyScopeSchema to have already
//   run in that db, so this must be called after
//   registerContextEngineHandlers), and the daemon's runtime.Clock.
// Outputs: a real jobs.ReachabilityFn when a symbol graph is already
//   stored for this repository; a disclosed Manifest Skipped state
//   otherwise - never a fabricated graph.
// Constraints: never invents a SymbolGraph. RegisterReachability itself
//   fails closed on a nil graph, so an absent stored graph is reported as
//   Skipped, not papered over with an empty one.
// SPORT: internal/daemon (ADD, R-16.80).

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// WireReachability opens cascade.db's symbol-graph table (idempotent
// migration), looks up the current repository's stored SymbolGraph, and
// calls manifest.RegisterReachability with it when one exists. It closes
// its own connection before returning (this is a startup-only lookup, not
// a handler that needs a live connection afterward - jobs.ReachabilityFn
// itself is a closure over the *already-decoded* repo.Reachable value,
// which holds no db reference; see RegisterReachability's doc comment).
func WireReachability(ctx context.Context, manifest *Manifest, paths runtime.PathProvider, clock runtime.Clock) error {
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "daemon: reachability: create data dir")
	}
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "daemon: reachability: open cascade.db")
	}
	defer func() { _ = db.Close() }()

	backupDir := filepath.Join(paths.DataDir(), "backups")
	if err := repo.ApplyGraphSchema(ctx, db, migrate.SQLiteEmitter{}, clock, dbPath, backupDir); err != nil {
		return err
	}

	repositoryID := gitRootExec(ctx, paths.DataDir())
	store := repo.NewGraphStore(db)
	graph, _, ok, err := store.Get(ctx, repositoryID)
	if err != nil {
		return err
	}
	if !ok {
		manifest.Register(reachabilitySubsystem)
		manifest.Skipped(reachabilitySubsystem, "no stored symbol graph for "+repositoryID+" yet")
		return nil
	}
	_, err = manifest.RegisterReachability(&graph)
	return err
}
