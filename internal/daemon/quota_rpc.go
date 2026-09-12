package daemon

// Purpose: T0-daemon-composition-root -- registers fleet.quota.snapshot
//   (P1-E40-W9-S77-T3) on the daemon's RPC router, closing the gap this
//   phase's AGENT-BRIEF named by example (a registered RPC method with no
//   composition-root caller is a subsystem that ships built, tested, and
//   unreachable). internal/fleet/topology.RegisterHandlers had a test
//   caller only until this file.
// Inputs: the daemon's shared *rpc.Registry, its runtime.PathProvider and
//   runtime.Clock -- the same triple every sibling registerXHandler in
//   this package (context_scope.go, journal_rpc.go, attention_rpc.go)
//   already takes.
// Outputs: fleet.quota.snapshot bound to a real topology.Store/
//   topology.QuotaStore pair over a real modernc SQLite connection to
//   cascade.db. Returns the opened *sql.DB so the caller can close it
//   during daemon shutdown, mirroring RegisterContextScopeHandler's exact
//   contract.
// Constraints: opens its OWN second *sql.DB handle to cascade.db rather
//   than threading a shared handle through buildRPCServer's exported
//   signature -- see context_scope.go's header for why this is the
//   established tradeoff on this composition root (SQLite supports
//   concurrent connections to one file; ApplyMigrationSchema is
//   idempotent by contract). A nil PathProvider or Clock degrades to
//   registering nothing rather than opening a database at a guessed path.
// SPORT: internal/daemon (ADD, T0-daemon-composition-root, P1-E40-W9-S77-T3).

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// quotaClockAdapter satisfies both migrate.Clock and topology.Clock
// (each declares the identical Now() time.Time shape) over a
// runtime.Clock, so this file needs no second clock implementation.
type quotaClockAdapter struct{ clock runtime.Clock }

func (a quotaClockAdapter) Now() time.Time { return a.clock.Now() }

// RegisterFleetQuotaHandler opens cascade.db's fleet-topology-quota
// schema, applies it (idempotent), and registers fleet.quota.snapshot
// against registry. Returns the opened *sql.DB so the caller can close it
// during daemon shutdown; a non-nil error means no db was left open (this
// function closes its own db before returning an error).
func RegisterFleetQuotaHandler(registry *rpc.Registry, paths runtime.PathProvider, clock runtime.Clock) (*sql.DB, error) {
	if paths == nil || clock == nil {
		return nil, nil
	}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "daemon: fleet.quota.snapshot: create data dir")
	}
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "daemon: fleet.quota.snapshot: open cascade.db")
	}

	adaptedClock := quotaClockAdapter{clock: clock}
	backupDir := filepath.Join(paths.DataDir(), "backups")
	if err := topology.ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, adaptedClock, dbPath, backupDir); err != nil {
		_ = db.Close()
		return nil, err
	}

	topoStore := topology.NewStore(db)
	quotaStore := topology.NewQuotaStore(db)
	topology.RegisterHandlers(registry, quotaStore, topoStore, adaptedClock)
	return db, nil
}
