//go:build !windows

// Purpose: open the real Unix runtime store for backup CLI operations.
// Inputs: runtime paths and the injected clock.
// Outputs: the provider store, its raw SQL handle, and one closer.
// Constraints: reuses the daemon's migration/bootstrap composition root.
// SPORT: cmd.cascade.backup-store/ADD (P1-E19-W4-S42-T3).
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/runtime"
)

func openBackupRuntime(ctx context.Context, paths runtime.PathProvider, clock runtime.Clock) (*backupRuntime, error) {
	store, db, closeStore, err := openRuntimeStore(ctx, paths, clock)
	if err != nil {
		return nil, err
	}
	return &backupRuntime{Store: store, DB: db, DataDir: paths.DataDir(), Close: closeStore}, nil
}
