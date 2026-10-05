// Purpose: the data-source helpers `cascade doctor`'s mounts hand to checks
//
//	whose interfaces want something the CLI must open: the provider health
//	source and the node record store. Moved out of doctor_mounts.go
//	unchanged so the file the mount gate reads stays under the file-length
//	cap as more checks are registered in it.
//
// Inputs: a runtime.PathProvider (and a Clock for the node store).
// Outputs: a doctor.ProviderHealthSource and a *nodes.RecordStore.
// Constraints: a source that cannot be located answers nil/an error, never
//
//	a stand-in that makes the check report a healthy empty set.
//
// SPORT: DOCTOR_SECRETS_REGISTRATION: CHANGE (cmd/cascade doctor sources).

package main

import (
	"context"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
)

// providerHealthSourceFor returns the production doctor.ProviderHealthSource.
// paths is accepted for signature symmetry with this file's other
// providerFor helpers; the adapter resolves the real environment paths
// via productionProviderDeps(), the same composition root `cascade
// provider add` already uses.
func providerHealthSourceFor(paths runtime.PathProvider) doctor.ProviderHealthSource {
	return providerHealthSourceAdapter{paths: paths}
}

// ListProviderHealth implements doctor.ProviderHealthSource.
func (a providerHealthSourceAdapter) ListProviderHealth(ctx context.Context) ([]doctor.ProviderHealthRow, error) {
	deps := productionProviderDeps()
	if a.paths != nil {
		// providerDepsFor, NOT a field reassignment: NewCustody and Gate
		// capture the PathProvider in closures, so overwriting deps.Paths
		// alone would leave those two still resolving the real home.
		deps = providerDepsFor(a.paths)
	}
	store, err := openProviderStorage(ctx, deps)
	if err != nil {
		return nil, err
	}
	defer func() { _ = store.Close() }()

	recs, err := store.Registry.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]doctor.ProviderHealthRow, len(recs))
	for i, rec := range recs {
		rows[i] = doctor.ProviderHealthRow{Name: rec.Name, Status: string(rec.HealthStatus)}
	}
	return rows, nil
}

// RecoverProviderHealth implements doctor.ProviderHealthSource.
func (providerHealthSourceAdapter) RecoverProviderHealth(ctx context.Context, name string) (bool, error) {
	store, err := openProviderStorage(ctx, productionProviderDeps())
	if err != nil {
		return false, err
	}
	defer func() { _ = store.Close() }()
	return store.Health.RecoverProbe(ctx, name)
}

// nodesRecordStoreFor opens the file-backed device record store that
// `node serve` writes heartbeats into. It returns nil ON PURPOSE when the
// data directory cannot be resolved: nodes.HealthCheck reports a nil store
// as StatusError, which is the honest answer for "the records that decide
// this could not be located". Inventing an empty store here would print
// "no nodes enrolled" for an installation whose nodes are simply
// unreadable, which is the one wrong answer a fleet check can give.
func nodesRecordStoreFor(paths runtime.PathProvider, clock runtime.Clock) *nodes.RecordStore {
	dataDir := paths.DataDir()
	if dataDir == "" {
		return nil
	}
	return nodes.NewRecordStore(nodes.NewFileRecordBackend(dataDir), clock)
}
