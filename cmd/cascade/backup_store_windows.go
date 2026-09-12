//go:build windows

// Purpose: open the Windows backup registry while elevated verbs stay tier-2 refused.
// Inputs: runtime paths and the injected clock.
// Outputs: a working provider store for list and target add/list/remove.
// Constraints: no elevation bypass; the Windows authorizer refuses first.
// SPORT: cmd.cascade.backup-store/ADD (P1-E19-W4-S42-T3).
package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/sqlite"
)

func openBackupRuntime(ctx context.Context, paths runtime.PathProvider, clock runtime.Clock) (*backupRuntime, error) {
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "backup: create data directory")
	}
	var rawDB *sql.DB
	migrator := func(ctx context.Context, db *sql.DB) error {
		rawDB = db
		_, err := storage.Bootstrap(ctx, db, storage.BootstrapOpts{Clock: clock})
		return err
	}
	driver, err := sqlite.Open(ctx, filepath.Join(paths.DataDir(), "cascade.db"), sqlite.WithMigrator(migrator))
	if err != nil {
		return nil, err
	}
	return &backupRuntime{Store: driver, DB: rawDB, DataDir: paths.DataDir(), Close: func() { _ = driver.Close() }}, nil
}
