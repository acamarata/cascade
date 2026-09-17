//go:build !windows

// Purpose: the daemon's usage-accounting wiring — the jobs_usage schema on
// the real cascade.db, the per-provider counter store `cascade provider
// usage` reads, and a rate-card cost estimator over the provider registry.
//
// WHY THIS FILE EXISTS. All three seams were built, documented and left
// with no production caller: `internal/conductor`'s files_scope had no path
// to a database, and the composition root that was supposed to close the
// gap (P1-E11-W3-S22-T4) landed without doing it. The disclosure in
// usage_migration.go's header was accurate when written and went stale the
// day that ticket shipped, which is how a documented gap becomes a silent
// one. Every dispatch went uncounted and `cascade provider usage` reported
// an empty table on a machine that had been dispatching (R-14.283).
//
// Inputs: the daemon's PathProvider, Clock, and the already-open provider
// registry the cost estimator prices from.
// Outputs: a daemon.ConductorAccounting with all three seams populated.
// Constraints: the aggregate counters MUST land in the same
// provider-usage.db the CLI reads (provider_health_cmd.go's
// openProviderStorage), or the daemon would count into a file nothing
// reports from — the identical shape as the defect this closes.
//
// SPORT: cmd/cascade daemon usage wiring (ADD) — P1-E11-W3-S23-T7.
package main

import (
	"context"
	"database/sql"
	"path/filepath"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/providers/usage"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// wireConductorAccounting opens both accounting databases and returns the
// three seams populated.
func wireConductorAccounting(
	ctx context.Context, paths runtime.PathProvider, clock runtime.Clock, reg *registry.Registry,
) (daemon.ConductorAccounting, error) {
	dataDir := paths.DataDir()
	jobsDBPath := filepath.Join(dataDir, "cascade.db")
	jobsDB, err := sql.Open("sqlite", "file:"+jobsDBPath+"?_busy_timeout=5000")
	if err != nil {
		return daemon.ConductorAccounting{}, cascade.Wrap(cascade.KindUnavailable, err,
			"daemon: usage accounting: open cascade.db")
	}
	backupDir := filepath.Join(dataDir, "backups")
	if err := conductor.ApplyUsageMigrationSchema(
		ctx, jobsDB, migrate.SQLiteEmitter{}, clock, jobsDBPath, backupDir); err != nil {
		_ = jobsDB.Close()
		return daemon.ConductorAccounting{}, err
	}
	// The SAME file the CLI reads. A second usage database would make
	// every counter true and every report empty.
	usageDB, err := openMigratedDB(ctx, filepath.Join(dataDir, providerUsageDBFile),
		func(ctx context.Context, db *sql.DB) error {
			return usage.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", "")
		})
	if err != nil {
		_ = jobsDB.Close()
		return daemon.ConductorAccounting{}, err
	}
	return daemon.ConductorAccounting{
		Store:      conductor.NewUsageStore(jobsDB),
		Aggregator: usage.NewManager(usageDB, clock),
		Cost:       rateCardEstimator{reg: reg},
	}, nil
}

// rateCardEstimator prices a dispatch from the registry's own rate card.
//
// A provider with no rate card returns 0, and that is the one number here
// that needs saying out loud: the registry's `Cost` is nil for "never
// fetched", so 0 means "cascade does not know what this cost", NOT "this
// was free". `cascade provider list` reports that distinction in its cost
// column so the two are separable where an operator reads them; a
// per-dispatch row has nowhere to carry it, and inventing an estimate
// would be worse than a zero an operator can cross-check.
type rateCardEstimator struct {
	reg *registry.Registry
}

// EstimateCostMicroUSD implements conductor.CostEstimator.
func (e rateCardEstimator) EstimateCostMicroUSD(providerName, model string, tokensIn, tokensOut int64) int64 {
	if e.reg == nil {
		return 0
	}
	rec, err := e.reg.GetProvider(context.Background(), providerName)
	if err != nil || rec.Cost == nil {
		return 0
	}
	price, ok := rec.Cost.Models[model]
	if !ok {
		return 0
	}
	return tokensIn*price.InputMicroUSDPerToken + tokensOut*price.OutputMicroUSDPerToken
}
