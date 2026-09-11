// Purpose: `cascade provider list|test|remove|health|usage`
//   (07-CLI-COMMAND-TREE §provider) and the provider_health doctor check's
//   shared storage wiring. This is the first production caller for the
//   durable S-20.T2 registry (internal/providers/registry), the S-20.T3
//   health manager (internal/providers/health), and the S-20.T4 usage
//   domain (internal/providers/usage) -- each package shipped fully
//   implemented and tested with no composition-root caller until this
//   ticket (see internal/build/testonly-allow.json's now-retired entries
//   for registry.ApplyMigrationSchema, health.NewManager/NewScheduler/
//   DefaultIntervals, usage.ApplyMigrationSchema/NewManager/NewReader).
//
// Inputs: cobra args/flags, plus providerDeps (provider_cmd.go) for the
//   vault/egress seams a test substitutes.
// Outputs: process output via internal/output.Writer, exactly like
//   provider_cmd.go's `add`.
// Constraints: no daemon RPC. The Go client SDK (internal/client) exposes
//   only Status as of this ticket, and internal/rpc has no provider
//   methods registered -- this ticket's files_scope does not include
//   either package, so these five subcommands operate directly against
//   local storage, matching `provider add`'s own already-local
//   architecture (see this ticket's journal for the full contradiction
//   note). Each subcommand opens its own dedicated SQLite file under the
//   data directory (providers.db, provider-usage.db, provider-events.db)
//   rather than the shared cascade.db a future composition-root ticket
//   may consolidate onto -- registry.go's own doc comment names that
//   consolidation as separately out of scope.
// SPORT: provider · J · S-21 · T-2 · provider CLI commands and doctor
//   check (P1-E10-W3-S21-T2).

package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/providers/health"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/providers/usage"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// The three dedicated SQLite files this ticket opens under the data
// directory. Kept separate from cascade.db (which registry.go's own doc
// comment says a later composition-root ticket owns consolidating onto)
// so this ticket needs no change to cmd/cascade/daemon_unix_store.go,
// which is outside files_scope.
const (
	providerRegistryDBFile = "providers.db"
	providerUsageDBFile    = "provider-usage.db"
	providerEventsDBFile   = "provider-events.db"
)

// providerStorage bundles the opened registry/usage/health handles the
// five subcommands and the doctor check share, plus every *sql.DB this
// call opened, closed together by Close.
type providerStorage struct {
	Registry *registry.Registry
	Usage    *usage.Manager
	Health   *health.Manager
	closers  []func() error
}

// Close closes every resource this providerStorage opened, returning the
// first error encountered (closing continues past an error so a leak in
// one file never masks another's).
func (s *providerStorage) Close() error {
	var firstErr error
	for _, c := range s.closers {
		if err := c(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// openProviderStorage opens (creating and migrating on first use) the
// registry, usage and events databases under deps.Paths.DataDir(), and
// returns a ready providerStorage. The caller MUST Close it.
func openProviderStorage(ctx context.Context, deps providerDeps) (*providerStorage, error) {
	dataDir := deps.Paths.DataDir()
	if dataDir == "" {
		return nil, cascade.New(cascade.KindUnavailable, "provider: could not resolve the cascade data directory")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "provider: create data directory")
	}
	clock := runtime.NewSystemClock()

	regDB, err := openMigratedDB(ctx, filepath.Join(dataDir, providerRegistryDBFile),
		func(ctx context.Context, db *sql.DB) error {
			return registry.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", "")
		})
	if err != nil {
		return nil, err
	}
	usageDB, err := openMigratedDB(ctx, filepath.Join(dataDir, providerUsageDBFile),
		func(ctx context.Context, db *sql.DB) error {
			return usage.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", "")
		})
	if err != nil {
		_ = regDB.Close()
		return nil, err
	}
	eventStore, err := runtime.OpenEmbeddedWriteStore(ctx, filepath.Join(dataDir, providerEventsDBFile), nil, nil)
	if err != nil {
		_ = regDB.Close()
		_ = usageDB.Close()
		return nil, err
	}

	reg := registry.NewRegistry(regDB, clock)
	bus := events.New(eventStore, clock)
	httpDeps := deps.HealthHTTPDoer
	if httpDeps == nil {
		httpDeps = realHTTPDoer{client: &http.Client{Timeout: providerDoctorTimeout}}
	}
	prober := httpReachabilityProber{doer: httpDeps}
	mgr := health.NewManager(reg, bus, clock, 0).WithEgress(deps.egressEngineForHealth(), prober)

	closers := []func() error{regDB.Close, usageDB.Close, eventStore.Close}
	return &providerStorage{Registry: reg, Usage: usage.NewManager(usageDB, clock), Health: mgr, closers: closers}, nil
}

// openMigratedDB opens a single-connection SQLite database at path and
// runs apply against it, closing db and returning apply's error on
// failure so the caller never holds a half-migrated handle.
func openMigratedDB(ctx context.Context, path string, apply func(context.Context, *sql.DB) error) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "provider: open %s", path)
	}
	db.SetMaxOpenConns(1)
	if err := apply(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// egressEngineForHealth builds the same provider-intake egress engine
// `provider add` uses (buildProviderEgressEngine in provider_cmd.go),
// gating the reachability prober behind the same egress class.
func (deps providerDeps) egressEngineForHealth() *egress.Engine {
	broker, err := providerVaultBroker(deps)
	if err != nil {
		return nil
	}
	engine, err := buildProviderEgressEngine(broker)
	if err != nil {
		return nil
	}
	return engine
}

// providerVaultBroker builds the secrets.Broker `provider add` already
// builds inline (buildIntakeDeps in provider_cmd.go); factored out here so
// remove's revocation path and health's egress gate share one
// construction rather than duplicating it.
func providerVaultBroker(deps providerDeps) (*secrets.Broker, error) {
	custody, err := deps.NewCustody()
	if err != nil {
		return nil, err
	}
	return secrets.NewBroker(custody, deps.Gate)
}

// mountProviderQueryCmds attaches list/test/remove/health/usage under the
// existing `provider` command tree (newProviderCmd, provider_cmd.go).
func mountProviderQueryCmds(cmd *cobra.Command, deps providerDeps) {
	cmd.AddCommand(newProviderListCmd(deps))
	cmd.AddCommand(newProviderTestCmd(deps))
	cmd.AddCommand(newProviderRemoveCmd(deps))
	cmd.AddCommand(newProviderHealthCmd(deps))
	cmd.AddCommand(newProviderUsageCmd(deps))
}

// providerListRow is one `cascade provider list` row (table and --json).
type providerListRow struct {
	Name         string                `json:"name"`
	Driver       string                `json:"driver"`
	Tier         string                `json:"tier"`
	Health       string                `json:"health"`
	Lanes        int                   `json:"lanes"`
	Capabilities provider.Capabilities `json:"capabilities"`
}

func (r providerListRow) String() string {
	return fmt.Sprintf("%-20s %-14s %-8s %-10s lanes=%d", r.Name, r.Driver, r.Tier, r.Health, r.Lanes)
}

type providerListResult struct{ Providers []providerListRow }

func (r providerListResult) String() string {
	if len(r.Providers) == 0 {
		return "no providers registered"
	}
	lines := make([]string, len(r.Providers))
	for i, p := range r.Providers {
		lines[i] = p.String()
	}
	return strings.Join(lines, "\n")
}

func newProviderListCmd(deps providerDeps) *cobra.Command {
	return &cobra.Command{
		Use:         "list",
		Short:       "List every registered provider",
		Annotations: map[string]string{"local": "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runProviderList(cmd, deps)
		},
	}
}

func runProviderList(cmd *cobra.Command, deps providerDeps) error {
	store, err := openProviderStorage(cmd.Context(), deps)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	recs, err := store.Registry.ListProviders(cmd.Context())
	if err != nil {
		return err
	}
	lanes, err := store.Registry.ListLanes(cmd.Context())
	if err != nil {
		return err
	}
	laneCount := map[string]int{}
	for _, l := range lanes {
		laneCount[l.ProviderName]++
	}
	rows := make([]providerListRow, len(recs))
	for i, rec := range recs {
		rows[i] = providerListRow{
			Name: rec.Name, Driver: string(rec.Driver), Tier: string(rec.Tier),
			Health: string(rec.HealthStatus), Lanes: laneCount[rec.Name], Capabilities: rec.Capabilities,
		}
	}
	return vaultOutputWriter(cmd).Result(providerListResult{Providers: rows})
}
