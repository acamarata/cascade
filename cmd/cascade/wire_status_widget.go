//go:build !windows

// Purpose: registers the status-widget daemon wiring (phaseOptions, after the
//
//	options registration): status.widget over the provider registry
//	(providers.db, opened through openMigratedDB as the conductor
//	registration does), the node records, the attention queue and the jobs
//	count, plus the supervised refresh loop that publishes
//	status.widget_changed every 10 s.
//
// Inputs: daemonWiring (Ctx, Registry, Manifest, Store, Bus, Clock, Logger,
//
//	Paths, Deps.Config for [widget].show_project_names).
//
// Outputs: status.widget on the registry; the status-widget-refresh manifest
//
//	entry.
//
// Constraints: the loop starts only through w.Manifest.GoSupervised under
//
//	w.Ctx, so Manifest.Wait joins it before the store closes; the same
//	goroutine closes the providers.db handle and the jobs handle when the
//	run context ends. This file is the only caller of
//	daemon.RunStatusWidgetRefresh. The provider source is passed into
//	RegisterStatusWidgetHandler, never set on its deps afterwards. A daemon
//	with no store registers nothing and reports Skipped. This replaces the
//	withStatusWidgetHandler rpcServerOption, whose hook had no run context or
//	Manifest (P1-WID-08); the RPC method set is unchanged.
//
// SPORT: cmd/cascade daemon registrations (P1-WID-08).
package main

import (
	"context"
	"database/sql"
	"path/filepath"

	"github.com/acamarata/cascade/internal/daemon"
	providerregistry "github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
)

// statusWidgetTicker builds the refresh ticker. A test replaces it to step
// the loop; production never does.
var statusWidgetTicker = func() runtime.Ticker {
	return runtime.NewSystemTicker(daemon.StatusWidgetRefreshInterval)
}

var _ = registerDaemonWiring(daemonRegistration{
	Name: "status-widget", Phase: phaseOptions, Order: 20,
	Wire: wireStatusWidget,
})

// wireStatusWidget is the status-widget registration: open the provider
// registry, register status.widget over it, start the supervised refresh.
func wireStatusWidget(w *daemonWiring) error {
	if w.Store == nil {
		w.Manifest.Skipped(daemon.StatusWidgetRefreshSubsystem, "no daemon store")
		return nil
	}
	regDB, err := openMigratedDB(w.Ctx, filepath.Join(w.Paths.DataDir(), providerRegistryDBFile),
		func(ctx context.Context, db *sql.DB) error {
			return providerregistry.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, w.Clock, "", "")
		})
	if err != nil {
		return err
	}
	showNames := w.Deps.Config != nil && w.Deps.Config.Widget.ShowProjectNames
	deps, err := daemon.RegisterStatusWidgetHandler(w.Ctx, w.Registry, w.Store, providerregistry.NewRegistry(regDB, w.Clock),
		w.Clock, w.Bus, w.Paths, func() bool { return showNames })
	if err != nil {
		_ = regDB.Close()
		return err
	}
	ticker := statusWidgetTicker()
	w.Manifest.GoSupervised(w.Ctx, daemon.StatusWidgetRefreshSubsystem, "every "+daemon.StatusWidgetRefreshInterval.String(),
		func(ctx context.Context) error {
			defer func() { _ = deps.Close(); _ = regDB.Close() }()
			return daemon.RunStatusWidgetRefresh(ctx, deps, w.Bus, ticker, w.Logger)
		})
	return nil
}
