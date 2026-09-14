//go:build !windows

// Purpose: open the embedded plugin-metadata store on POSIX, reusing
// daemon_unix_store.go's openRuntimeStore composition root exactly (the
// same cascade.db, the same migration path the daemon itself uses) rather
// than a second, independently-maintained bootstrap sequence.
//
// SPORT: cli/plugin-lifecycle/ADD (P1-E15-W4-S32-T4).
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// openPluginStore returns a ready provider.Store plus its closer.
func openPluginStore(ctx context.Context, paths runtime.PathProvider, clock runtime.Clock) (provider.Store, func(), error) {
	store, _, closeStore, err := openRuntimeStore(ctx, paths, clock)
	if err != nil {
		return nil, nil, err
	}
	return store, closeStore, nil
}
