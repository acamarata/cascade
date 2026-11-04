//go:build !windows

// Purpose: the composition-root call site R-16.80 Ruling 2 assigns:
//
//	builds the real conductor.execute collaborators (the durable
//	providers registry, the daemon's configured QuotaPolicy, and the
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
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/nodes"
	providerdispatch "github.com/acamarata/cascade/internal/providers/dispatch"
	"github.com/acamarata/cascade/internal/providers/gateway"
	providerregistry "github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/provider"
	providertransport "github.com/acamarata/cascade/providers/transport"
)

// wireConductorAndReachability is buildRPCServer's single call site for
// both R-16.80 connectors. A nil store leaves both unregistered (see this
// file's header comment); any other failure is propagated, matching every
// sibling registerXHandler's own error-propagation convention.
func wireConductorAndReachability(ctx context.Context, registry *rpc.Registry, manifest *daemon.Manifest, paths runtime.PathProvider, clock runtime.Clock, store provider.Store, tunnels nodes.TunnelStateLookup, routerOpts ...conductor.RouterOption) (daemon.ConductorFanOut, error) {
	if store == nil {
		return daemon.ConductorFanOut{}, nil
	}
	fanOut, err := wireConductorExecute(ctx, registry, manifest, paths, clock, store, tunnels, routerOpts...)
	if err != nil {
		return fanOut, err
	}
	return fanOut, daemon.WireReachability(ctx, manifest, paths, clock)
}

// wireConductorExecute opens the same durable providers.db `provider
// add/list/health` already use (openMigratedDB, provider_health_cmd.go),
// builds a real registry.Reader, takes the daemon's one QuotaPolicy (built
// from [conductor.quota] over the HotReloader's current config and
// re-applied on every accepted reload, conductor_deny_store.go), builds a
// real conductor.ProviderResolver (dispatch.NewResolver,
// DEFECT-conductor-execute-permanently-unavailable.md), then calls
// daemon.RegisterConductorExecuteHandler with all of them plus the daemon's
// real audit.Writer.
//
// The resolver's CredentialSource is the STANDING-GRANT reader
// (R-14.243, daemon_unix_conductor_security.go's daemonCredentialSource).
// It used to be nil, because "how does a headless daemon obtain vault
// access with no interactive elevation prompt" was an open question; it is
// answered now — a human issues `cascade vault grant <name>` through the
// same elevation gate `vault get` uses, scoped to one key and one verb,
// expiring, revocable and audited. With no live grant the behaviour is
// unchanged from before: a typed KindUnavailable naming the key, refused
// at the true credential boundary per request, never a prompt from a
// process with nobody to answer it.
func wireConductorExecute(ctx context.Context, registry *rpc.Registry, manifest *daemon.Manifest, paths runtime.PathProvider, clock runtime.Clock, store provider.Store, tunnels nodes.TunnelStateLookup, routerOpts ...conductor.RouterOption) (daemon.ConductorFanOut, error) {
	reg, err := openDaemonProviderRegistry(ctx, paths, clock)
	if err != nil {
		return daemon.ConductorFanOut{}, err
	}
	reader := providerregistry.NewReader(reg)
	live, err := conductorPolicyFor(paths, clock)
	if err != nil {
		return daemon.ConductorFanOut{}, err
	}
	quota := live.Quota
	credentials, cerr := daemonCredentialSource(paths)
	if cerr != nil {
		return daemon.ConductorFanOut{}, cerr
	}
	security, serr := conductorSecurity(paths)
	if serr != nil {
		return daemon.ConductorFanOut{}, serr
	}
	// After conductorSecurity, which binds the process firewall the health
	// probes run behind. One Manager serves the resolver's transient
	// counter, the scheduler and the retry loop's lane eligibility.
	// Gateway tokens come from the start snapshot (P1-GWY-10, the reader
	// daemonGateways takes from daemonSnapshotGetenv), never the process
	// environment the daemon scrubbed at start.
	cfg := admittedDaemonConfig(paths)
	healthMgr, herr := startDaemonProviderHealth(ctx, manifest, reg, store, clock, cfg)
	if herr != nil {
		return daemon.ConductorFanOut{}, herr
	}
	resolver, err := newDaemonResolver(reg, credentials, clock, providertransport.NewHTTPTransport(&http.Client{}), cfg, providerdispatch.WithHealth(healthMgr))
	if err != nil {
		return daemon.ConductorFanOut{}, err
	}
	accounting, aerr := wireConductorAccounting(ctx, paths, clock, reg)
	if aerr != nil {
		return daemon.ConductorFanOut{}, aerr
	}
	// After conductorSecurity, which binds the process firewall to this
	// daemon's data directory: the audit log opens over the same vault.
	auditWriter, err := openDaemonAuditLog(store, clock, nil, paths)
	if err != nil {
		return daemon.ConductorFanOut{}, err
	}
	// Durable fan-out over the daemon store itself (no second connection):
	// one leg budget and one claim table per daemon, returned so the
	// registration stores this same value for the resume registration.
	fanOut := daemon.NewConductorFanOut(store, clock)
	return fanOut, daemon.RegisterConductorExecuteHandler(registry, manifest, reader, quota, resolver, auditWriter, clock,
		security, accounting, fanOut,
		conductorRouterOptionsEligible(tunnels, paths, clock, laneEligibleByHealth(reg, healthMgr), routerOpts)...)
}

// openDaemonProviderRegistry opens the durable providers.db the `provider`
// verbs already use, migrated, as a registry.
func openDaemonProviderRegistry(ctx context.Context, paths runtime.PathProvider, clock runtime.Clock) (*providerregistry.Registry, error) {
	regDB, err := openMigratedDB(ctx, filepath.Join(paths.DataDir(), providerRegistryDBFile),
		func(ctx context.Context, db *sql.DB) error {
			return providerregistry.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", "")
		})
	if err != nil {
		return nil, err
	}
	return providerregistry.NewRegistry(regDB, clock), nil
}

// newDaemonResolver builds the conductor's provider resolver: the
// standing-grant credentials for vault refs and, for gateway provider
// records, the gateway accounts ADMITTED in cfg (EffectiveConfigOf, the
// accepted baseline, never the raw file) with a call-time token source of
// this daemon process (P1-GWY-08). The accounts are read once here: an
// account accepted later applies at the next start. A nil cfg admits none.
func newDaemonResolver(reg providerdispatch.RegistryLookup, credentials providerdispatch.CredentialSource, clock runtime.Clock, tr providertransport.Transport, cfg *runtime.Config, opts ...providerdispatch.ResolverOption) (*providerdispatch.Resolver, error) {
	accounts, src, err := daemonGateways(cfg, nil)
	if err != nil {
		return nil, err
	}
	return providerdispatch.NewResolver(reg, credentials, clock, tr, append([]providerdispatch.ResolverOption{providerdispatch.WithGatewayAccounts(accounts, src)}, opts...)...)
}

// daemonGateways returns the admitted gateway accounts of cfg and the
// call-time token source of this daemon process, shared by the resolver, the
// health prober and the quota pollers so all see one admitted set. Every
// variable the source reads goes through getenv, the start snapshot
// (credentialEnv.Getenv). A nil getenv reads the snapshot this process's
// daemon published (daemonSnapshotGetenv); with none (no composed daemon, a
// bare wiring test) it is an empty snapshot, so every name falls through.
func daemonGateways(cfg *runtime.Config, getenv func(string) string) ([]gateway.Account, gateway.Source, error) {
	accounts, err := gateway.AccountsFrom(runtime.EffectiveConfigOf(cfg))
	if err != nil {
		return nil, gateway.Source{}, err
	}
	if getenv == nil {
		getenv = daemonSnapshotGetenv()
	}
	home, _ := os.UserHomeDir()
	return accounts, gateway.Source{Getenv: getenv, Home: home, User: getenv("USER")}, nil
}

// admittedDaemonConfig returns the live (admitted) config of the composed
// daemon whose config path is paths', or nil when no daemon was composed
// over it (a bare wiring test): nil admits no gateway account.
func admittedDaemonConfig(paths runtime.PathProvider) *runtime.Config {
	if paths == nil {
		return nil
	}
	livePolicies.mu.Lock()
	owner := livePolicies.owner[paths.ConfigPath()]
	livePolicies.mu.Unlock()
	if owner == nil || owner.reloader == nil {
		return nil
	}
	return owner.reloader.Current()
}

// conductorRouterOptions is the router wiring the daemon applies: the node
// placement seam first, then transient retry, then extra (the
// restricted-permit option). One helper so the production wiring and the
// resume tests' deny wiring cannot differ.
func conductorRouterOptions(tunnels nodes.TunnelStateLookup, paths runtime.PathProvider, clock runtime.Clock, extra []conductor.RouterOption) []conductor.RouterOption {
	return conductorRouterOptionsEligible(tunnels, paths, clock, nil, extra)
}

// conductorRouterOptionsEligible is conductorRouterOptions with the retry
// loop's lane-eligibility seam (provider health, P1-GWY-09); nil means every
// lane is always eligible.
func conductorRouterOptionsEligible(tunnels nodes.TunnelStateLookup, paths runtime.PathProvider, clock runtime.Clock, eligible conductor.LaneEligible, extra []conductor.RouterOption) []conductor.RouterOption {
	placement := conductor.NodePlacement(nodes.Engine{Tunnels: tunnels}, nodeRecordStore(paths, clock))
	return append([]conductor.RouterOption{placement, daemonRetryOption(clock, eligible)}, extra...)
}

// daemonRetryOption applies SetRetry to the Executor built over the
// daemon's router (P1-GWY-03): the default policy on the daemon clock, a
// pause that ends early with its context, and a source seeded once from the
// clock. eligible is the provider-health seam (laneEligibleByHealth).
func daemonRetryOption(clock runtime.Clock, eligible conductor.LaneEligible) conductor.RouterOption {
	seed := uint64(clock.Now().UnixNano())
	return conductor.RetryOption(conductor.DefaultRetryPolicy(), clock, contextSleep, rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)), eligible)
}

// contextSleep pauses for d on a timer, returning ctx.Err() if ctx ends first.
func contextSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// restrictedPermitsOption reads [conductor] restricted_external_lanes from
// the daemon's loaded config and returns the router option that applies it.
// It is read ONCE, here, at conductor construction: a changed list applies at
// the next start. A nil config or an absent key is the empty list (every
// external lane refused for restricted content); a malformed list (wildcard,
// non-string entry) refuses KindInvalidInput and the daemon does not start
// with a permit list it cannot read exactly.
func restrictedPermitsOption(cfg *runtime.Config) (conductor.RouterOption, error) {
	var section map[string]interface{}
	if cfg != nil {
		section, _ = cfg.Extra["conductor"].(map[string]interface{})
	}
	permits, err := conductor.ParseRestrictedPermits(section)
	if err != nil {
		return nil, err
	}
	return conductor.WithRestrictedPermits(permits), nil
}

// nodeRecordStore opens the enrolled-node record store over the SAME
// data-dir file every `node` verb already uses (node_serve_composition.go,
// node_keys.go, daemon_unix_run_fleetjobs.go all build it this way), so
// placement decides over the one real enrolled fleet rather than a second,
// parallel view of it.
func nodeRecordStore(paths runtime.PathProvider, clock runtime.Clock) *nodes.RecordStore {
	return nodes.NewRecordStore(nodes.NewFileRecordBackend(paths.DataDir()), clock)
}
