//go:build windows

// Purpose: open the embedded plugin-metadata store on Windows, mirroring
// backup_store_windows.go's exact pattern (storage.Bootstrap directly,
// since daemon_unix_store.go's richer migrator is !windows-tagged) —
// list/info/enable/disable/remove have no elevation and no daemon
// dependency on any platform (§5.14 lists none of them), so they must work
// here even though the elevated verbs are tier-2 refused
// (plugin_platform_windows.go) before ever reaching this file.
//
// SPORT: cli/plugin-lifecycle/ADD (P1-E15-W4-S32-T4).
package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
)

// openPluginStore returns a ready provider.Store plus its closer.
func openPluginStore(ctx context.Context, paths runtime.PathProvider, clock runtime.Clock) (provider.Store, func(), error) {
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "plugin: create data directory")
	}
	migrator := func(ctx context.Context, db *sql.DB) error {
		_, err := storage.Bootstrap(ctx, db, storage.BootstrapOpts{Clock: clock})
		return err
	}
	driver, err := sqlite.Open(ctx, filepath.Join(paths.DataDir(), "cascade.db"), sqlite.WithMigrator(migrator))
	if err != nil {
		return nil, nil, err
	}
	return driver, func() { _ = driver.Close() }, nil
}
