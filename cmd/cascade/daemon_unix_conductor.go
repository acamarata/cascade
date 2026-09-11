//go:build !windows

// Purpose: the composition-root call site R-16.80 Ruling 2 assigns:
//
//	builds the real conductor.execute collaborators (the durable
//	providers registry, a fail-closed default QuotaPolicy, and the
//	daemon's real audit.Writer) and calls daemon.
//	RegisterConductorExecuteHandler and daemon.WireReachability so
//	"conductor.execute" and Manifest.RegisterReachability both get a
//	real production caller for the first time (previously: zero).
//
// Inputs: the *rpc.Registry and *daemon.Manifest buildRPCServer already
//
//	builds, paths/clock already threaded through every sibling
//	registerXHandler call, and the real provider.Store buildRPCServer's
//	caller opened.
//
// Outputs: "conductor.execute" registered on registry; a real
//
//	*conductor.DefaultRouter and *conductor.Executor (or its real
//	construction failure) recorded on manifest; jobs.reachability
//	likewise recorded, Skipped when no symbol graph is stored yet.
//
// Constraints: a nil store (some existing minimal buildRPCServer test
//
//	fixtures) skips this wiring entirely rather than opening audit.New
//	over a store that does not exist -- the same guard
//	RegisterRecallIndexHandler's own call site already applies for the
//	identical reason (daemon_unix_run.go's own comment on that call).
//
// SPORT: cmd/cascade/daemon (ADD, R-16.80).
package main

import (
	"context"
	"database/sql"
	"path/filepath"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/daemon"
	providerregistry "github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/provider"
)

// wireConductorAndReachability is buildRPCServer's single call site for
// both R-16.80 connectors. A nil store leaves both unregistered (see this
// file's header comment); any other failure is propagated, matching every
// sibling registerXHandler's own error-propagation convention.
func wireConductorAndReachability(ctx context.Context, registry *rpc.Registry, manifest *daemon.Manifest, paths runtime.PathProvider, clock runtime.Clock, store provider.Store) error {
	if store == nil {
		return nil
	}
	if err := wireConductorExecute(ctx, registry, manifest, paths, clock, store); err != nil {
		return err
	}
	return daemon.WireReachability(ctx, manifest, paths, clock)
}

// wireConductorExecute opens the same durable providers.db `provider
// add/list/health` already use (openMigratedDB, provider_health_cmd.go),
// builds a real registry.Reader and a fail-closed default QuotaPolicy
// (no [conductor.quota] TOML section is parsed at this call site yet --
// a disclosed, narrower gap than R-16.80's connector fix, since
// ParseQuotaConfig(nil) itself already fails closed to a safe single-lane
// default rather than an error), and calls
// daemon.RegisterConductorExecuteHandler with them plus the daemon's real
// audit.Writer. resolver is nil: no production conductor.ProviderResolver
// implementation exists anywhere in this tree yet (internal/build/
// testonly-allow.json's internal/conductor.NewExecutor entry).
func wireConductorExecute(ctx context.Context, registry *rpc.Registry, manifest *daemon.Manifest, paths runtime.PathProvider, clock runtime.Clock, store provider.Store) error {
	regDB, err := openMigratedDB(ctx, filepath.Join(paths.DataDir(), providerRegistryDBFile),
		func(ctx context.Context, db *sql.DB) error {
			return providerregistry.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", "")
		})
	if err != nil {
		return err
	}
	reg := providerregistry.NewRegistry(regDB, clock)
	reader := providerregistry.NewReader(reg)
	cfg, _, err := conductor.ParseQuotaConfig(nil)
	if err != nil {
		return err
	}
	quota := conductor.NewQuotaPolicy(cfg, clock)
	auditWriter := audit.New(store, clock, nil)
	return daemon.RegisterConductorExecuteHandler(registry, manifest, reader, quota, nil, auditWriter, clock)
}
