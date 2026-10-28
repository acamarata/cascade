package daemon

// Purpose: the fleet runtime's reservation step. Over ONE cascade.db handle
//
//	it builds the jobs store, the ONE LeaseManager (controller authority
//	resolved from the binding on every call), the fenced WorktreeManager and
//	the ONE economics Reserver. StartReservations runs the fenced start Sweep
//	and then the supervised liveness loop.
//
// Inputs: FleetRuntimeDeps. Outputs: the FleetRuntime accessors below.
// Constraints: no second handle, LeaseManager, WorktreeManager or Reserver in
// daemon code. The Reserver's seams live in subsystems_fleet_seams.go.
//
// SPORT: internal/daemon (ADD, P1-CAP-10).

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The manifest rows the reservation step owns.
const (
	fleetReservationSweepSubsystem = "reservation-sweep"
	fleetLivenessSubsystem         = "fleet-reservation-liveness"
)

// Test seams: the liveness ticker, the lease-holder liveness probe and the
// handle close (counted by the close-order test).
var (
	newLivenessTicker = func(period time.Duration) runtime.Ticker { return runtime.NewSystemTicker(period) }
	newLeaseProbe     = jobs.NewProcessLivenessProbe
	closeJobsDB       = func(db *sql.DB) error { return db.Close() }
)

// fleetReservation is what the reservation step builds and owns.
type fleetReservation struct {
	paths        runtime.PathProvider
	clock        runtime.Clock
	logger       *slog.Logger
	isController func() bool
	db           *sql.DB
	jobs         *jobs.Store
	leases       *jobs.LeaseManager
	worktrees    *jobs.WorktreeManager
	resStore     *economics.ReservationStore
	reserver     *economics.Reserver
	quota        *topology.QuotaStore
	topo         *topology.Store
	attention    *supervision.Store
	projects     *economics.ActiveProjectCount
	probe        jobs.ProcessLivenessProbe
	epoch        string
	stop         chan struct{}
	started      atomic.Bool // StartReservations ran
}

// JobsDB returns the runtime's one cascade.db handle (MaxOpenConns 1).
func (r *FleetRuntime) JobsDB() *sql.DB { return r.res.db }

// Jobs returns the one jobs store.
func (r *FleetRuntime) Jobs() *jobs.Store { return r.res.jobs }

// Leases returns the one LeaseManager.
func (r *FleetRuntime) Leases() *jobs.LeaseManager { return r.res.leases }

// Projects returns the one project counter the Reserver's share rule reads.
func (r *FleetRuntime) Projects() *economics.ActiveProjectCount { return r.res.projects }

// Worktrees returns the one WorktreeManager, fenced by Leases().Fence.
func (r *FleetRuntime) Worktrees() *jobs.WorktreeManager { return r.res.worktrees }

// Reserver returns the one Reserver.
func (r *FleetRuntime) Reserver() *economics.Reserver { return r.res.reserver }

// Quota returns the topology quota store over the runtime handle.
func (r *FleetRuntime) Quota() *topology.QuotaStore { return r.res.quota }

// OwnerEpoch returns the owner epoch minted for this daemon start.
func (r *FleetRuntime) OwnerEpoch() string { return r.res.epoch }

// IsController reports, at call time, whether this daemon holds the
// controller role: the check the LeaseManager makes on every call.
func (r *FleetRuntime) IsController() bool { return r.res.isController() }

// buildReservation is the reservation step. The handle's close is registered
// first, so a later failure closes it with the rest of the runtime.
func (r *FleetRuntime) buildReservation(ctx context.Context, d FleetRuntimeDeps) error {
	db, err := openJobsDB(ctx, d.Paths, d.Clock)
	if err != nil {
		return err
	}
	stop := make(chan struct{})
	r.addCloser(func() error { close(stop); return closeJobsDB(db) })
	epoch, err := mintOwnerEpoch()
	if err != nil {
		return err
	}
	f := &r.res
	*f = fleetReservation{
		paths: d.Paths, clock: d.Clock, logger: d.Logger, isController: roleIsController(d.Binding),
		db: db, jobs: jobs.NewStore(db), quota: topology.NewQuotaStore(db), topo: topology.NewStore(db),
		attention: NewAttentionStore(d.Store, d.Clock, d.Bus), projects: &economics.ActiveProjectCount{},
		probe: newLeaseProbe(), epoch: epoch, stop: stop,
	}
	fleetJournal := journal.New(d.Store, d.Clock, journal.DefaultNamespace)
	if err := f.buildLeases(d, fleetJournal); err != nil {
		return err
	}
	wt, _, err := d.Manifest.RegisterWorktreeSweep(ctx, f.jobs, fleetJournal, f.attention, f.probe, f.leases.Fence)
	if wt == nil {
		return err // no manager; a sweep error with a manager stays on its manifest row
	}
	f.worktrees = wt
	return f.buildReserver(r.Admission().Admit)
}

// openJobsDB opens cascade.db with the write-DSN form (IMMEDIATE transactions
// waiting under busy_timeout) and applies the jobs, outbox, reservation and
// topology schemas on that one handle.
func openJobsDB(ctx context.Context, paths runtime.PathProvider, clock runtime.Clock) (*sql.DB, error) {
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "fleet runtime: create data dir")
	}
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000&_txlock=immediate")
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "fleet runtime: open %s", dbPath)
	}
	db.SetMaxOpenConns(1)
	dialect := migrate.SQLiteEmitter{}
	for _, apply := range []func(context.Context, *sql.DB, migrate.Dialect, migrate.Clock, string, string) error{
		jobs.ApplyJobsSchema, jobs.ApplyOutboxSchema, economics.ApplyReservationSchema, topology.ApplyMigrationSchema,
	} {
		if err := apply(ctx, db, dialect, clock, "", ""); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return db, nil
}

// roleIsController resolves the role from the binding on every call, so a
// re-designation at run time changes the answer without a restart. A binding
// that cannot be read is not the controller (fail closed).
func roleIsController(binding nodes.ControllerBindingBackend) func() bool {
	return func() bool {
		role, err := nodes.ResolveRole(binding)
		return err == nil && role == nodes.RoleController
	}
}

// mintOwnerEpoch returns a fresh owner epoch for this daemon start.
func mintOwnerEpoch() (string, error) {
	var b [8]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "fleet runtime: mint owner epoch")
	}
	return fmt.Sprintf("daemon-%d-%x", os.Getpid(), b), nil
}

// buildLeases builds the event sink over the daemon bus, the fleet journal
// and the attention store, the one LeaseManager, and installs its
// release-on-terminal hook on the store.
func (f *fleetReservation) buildLeases(d FleetRuntimeDeps, j journal.Store) error {
	sink, err := jobs.NewLeaseEventSink(d.Bus, j, f.attention)
	if err != nil {
		return err
	}
	f.leases, err = jobs.NewLeaseManagerWithEvents(f.jobs, d.Clock, f.isController, jobs.DefaultLeaseDefaults(), sink)
	if err != nil {
		return err
	}
	jobs.WireLeaseRelease(f.jobs, f.leases)
	return nil
}

// buildReserver builds the one ReservationStore and Reserver with the
// production seams.
func (f *fleetReservation) buildReserver(permit economics.PermitFn) error {
	var err error
	if f.resStore, err = economics.NewReservationStore(f.db, f.clock); err != nil {
		return err
	}
	f.reserver, err = economics.NewReserver(f.resStore, f.clock, f.epoch, economics.ReserverSeams{
		Buckets: f.buckets, RenewLeases: f.renewLeases, Permit: permit,
		AcquireLeases: f.acquireLeases, ReleaseLeases: f.releaseLeases,
		AllocateWorktree: f.allocateWorktree, RemoveWorktree: f.removeWorktree,
		ValidateLeases: f.validateLeases, ReserveFraction: f.reserveFraction, Projects: f.projects,
		FindLeases: f.findLeases, FindWorktree: f.findWorktree, RaiseAttention: f.raiseAttention,
	})
	return err
}

// StartReservations runs the fenced start Sweep, refusing while another
// daemon is live on this home (the pidfile classified with prober, this
// process's own pid excluded), records its report as the reservation-sweep
// row, then starts the liveness loop on ctx; a second call is refused. Call it
// before the socket serves. Rows the sweep could not retire fail the row but
// not the start: the liveness loop's ExpireStale retries them.
func (r *FleetRuntime) StartReservations(ctx context.Context, prober ProcessProber) error {
	if prober == nil {
		return cascade.New(cascade.KindInvalidInput, "fleet runtime: StartReservations requires a process prober")
	}
	f := &r.res
	if !f.started.CompareAndSwap(false, true) {
		return cascade.New(cascade.KindConflict, "fleet runtime: reservations already started")
	}
	r.manifest.Register(fleetReservationSweepSubsystem)
	var liveErr error
	var sawLive bool
	live := func(context.Context) (bool, error) {
		sawLive, liveErr = otherDaemonLive(PIDFilePath(f.paths), prober, os.Getpid())
		return sawLive, liveErr
	}
	rep, err := f.reserver.Sweep(ctx, live)
	if err != nil {
		r.manifest.Failed(fleetReservationSweepSubsystem, err.Error())
		// Refuse on a live daemon, an unreadable pidfile, or a sweep that never
		// reached the rows (RollbackErrors counts only per-row failures).
		if liveErr != nil || sawLive || rep.RollbackErrors == 0 {
			return err
		}
	} else {
		r.manifest.Started(fleetReservationSweepSubsystem, fmt.Sprintf("rolled_back=%d adopted=%d expired=%d rollback_errors=%d owner_epoch=%s",
			rep.RolledBack, rep.Adopted, rep.Expired, rep.RollbackErrors, f.epoch))
	}
	interval := time.Duration(jobs.DefaultLeaseDefaults().RenewEverySeconds) * time.Second
	r.manifest.GoSupervised(ctx, fleetLivenessSubsystem, "renewing reservations and leases every "+interval.String(),
		f.superviseLiveness(newLivenessTicker(interval), interval))
	return nil
}

// superviseLiveness runs one liveness tick per ticker tick until the run
// context ends or the runtime closes.
func (f *fleetReservation) superviseLiveness(t runtime.Ticker, interval time.Duration) func(context.Context) error {
	return func(ctx context.Context) error {
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-f.stop:
				return nil
			case <-t.C():
				f.livenessTick(ctx, interval)
			}
		}
	}
}

// livenessTick heartbeats this process's reservations, retires stale ones
// (skipped only when the heartbeat write itself failed: an error with no
// lost lease), then, on the controller, expires overdue leases and
// reclaims those whose holder process is gone. Errors are logged; the loop
// keeps its interval.
func (f *fleetReservation) livenessTick(ctx context.Context, interval time.Duration) {
	_, lost, err := f.reserver.Heartbeat(ctx)
	errs := []error{err}
	if err == nil || lost > 0 {
		_, err = f.reserver.ExpireStale(ctx, interval)
		errs = append(errs, err)
	}
	if f.isController() {
		_, err = f.leases.SweepExpired(ctx)
		errs = append(errs, err)
		_, err = f.leases.ReclaimAll(ctx, f.probe)
		errs = append(errs, err)
	}
	if joined := errors.Join(errs...); joined != nil && f.logger != nil {
		f.logger.Warn("fleet reservation liveness tick", slog.String("error", joined.Error()))
	}
}
