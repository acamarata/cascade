// Purpose: the hydration doctor check's store opener, split out of
//
//	doctor_mounts.go under its own 300-line cap (Art.10.3).
//
// SPORT: cmd/cascade/doctor (ADD) — P1-E16-W4-S34-T4.
package main

import (
	"context"
	"path/filepath"

	"github.com/acamarata/cascade/internal/context/hydration"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
)

// hydrationStoreFor opens the store the hydration check reads, resolving
// it at RUN time rather than at registration: doctor's registry is built
// before anything guarantees a data directory exists, and a check that
// refused to register on a virgin HOME would be missing from exactly the
// report a first run needs.
//
// The store is the same cascade.db the hook publishes onto. A nil paths
// yields a typed refusal rather than a guess -- the check turns that into
// StatusError, which is the honest answer for a subject nobody could look
// at.
func hydrationStoreFor(paths runtime.PathProvider) hydration.StoreFunc {
	return func(ctx context.Context) (provider.Store, func(), error) {
		if paths == nil {
			return nil, nil, cascade.New(cascade.KindUnavailable,
				"cascade doctor: no data directory is resolved for the hydration event log")
		}
		store, err := sqlite.Open(ctx, filepath.Join(paths.DataDir(), "cascade.db"))
		if err != nil {
			return nil, nil, err
		}
		return store, func() { _ = store.Close() }, nil
	}
}
