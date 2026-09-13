// Purpose: T0-daemon-composition-root -- registers fleet.mode.show/set
//   (P1-E41-W9-S79-T2) on the daemon's RPC router, closing the gap this
//   phase's AGENT-BRIEF named by example (a registered RPC method with no
//   composition-root caller is a subsystem that ships built, tested and
//   unreachable). Mirrors quota_rpc.go's exact RegisterFleetQuotaHandler
//   shape: opens its OWN second *sql.DB handle to cascade.db.
//
// Inputs: the daemon's shared *rpc.Registry, its runtime.PathProvider and
//   runtime.Clock -- the same triple every sibling registerXHandler in
//   this package already takes.
// Outputs: fleet.mode.show/set bound to a real
//   economics.SchedulerModeStore over a real modernc SQLite connection to
//   cascade.db. Returns the opened *sql.DB so the caller can close it
//   during daemon shutdown, mirroring RegisterFleetQuotaHandler's exact
//   contract.
// Constraints: a nil PathProvider or Clock degrades to registering
//   nothing rather than opening a database at a guessed path. The
//   ProjectResolver seam (internal/fleet/economics.ProjectResolver) has
//   no real SessionScope-backed implementation at this composition root
//   yet -- see rpc_mode.go's own CONTRACT DEVIATION note -- so this file
//   passes nil, matching that seam's documented "unresolved project
//   returns a typed error, never a fallback" contract until a later
//   ticket wires a real resolver.
// SPORT: internal/daemon (ADD, T0-daemon-composition-root, P1-E41-W9-S79-T2).

package daemon

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// RegisterFleetModeHandler opens cascade.db's fleet-economics-mode
// schema, applies it (idempotent), and registers fleet.mode.show/set
// against registry. Returns the opened *sql.DB so the caller can close it
// during daemon shutdown; a non-nil error means no db was left open.
func RegisterFleetModeHandler(registry *rpc.Registry, paths runtime.PathProvider, clock runtime.Clock) (*sql.DB, error) {
	if paths == nil || clock == nil {
		return nil, nil
	}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "daemon: fleet.mode: create data dir")
	}
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "daemon: fleet.mode: open cascade.db")
	}

	adaptedClock := quotaClockAdapter{clock: clock}
	backupDir := filepath.Join(paths.DataDir(), "backups")
	if err := economics.ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, adaptedClock, dbPath, backupDir); err != nil {
		_ = db.Close()
		return nil, err
	}

	store := economics.NewSchedulerModeStore(db, adaptedClock)
	economics.RegisterHandlers(registry, store, nil, adaptedClock)
	return db, nil
}
