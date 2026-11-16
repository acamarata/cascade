//go:build !windows

// Purpose: binds the daemon's one fleet Reserver to every model dispatch and
//   to the scheduler's re-admit seam. The conductor's Executor reserves
//   through the dispatch slot daemon_unix_conductor.go installs; this
//   registration fills that slot with an adapter over FleetRuntime.Reserver.
// Inputs: w.Deps.Fleet (Reserver, Projects, the capacity DomainOf and the one
//   reservation store), w.Manifest, w.Paths, w.Clock.
// Outputs: the "fleet-dispatch" registration; the slot's CallReserver; the
//   manifest's scheduler reservation seam.
// Constraints: constructs no Reserver, Fleet, LeaseManager, WorktreeManager,
//   AdmissionController or sweep. Each Reserve takes one quota-only
//   interactive row whose Estimate is economics.EstimateFor over the
//   NaiveTokenCounter count of the inputs; a count or estimate error refuses
//   with no row written, never a default Estimate. Without a fleet runtime
//   the slot stays unbound (every dispatch refuses) and the scheduler seam
//   refuses every re-admit, so leased jobs wait and the daemon still starts.

package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

var _ = registerDaemonWiring(daemonRegistration{Name: "fleet-dispatch", Phase: phaseConductor, Order: 60, Wire: wireFleetDispatch})

// dispatchProject is the project of a dispatch whose request names no task.
const dispatchProject = "conductor"

var (
	// errDispatchUnbound refuses a dispatch on a daemon with no fleet runtime.
	errDispatchUnbound = cascade.Wrap(cascade.KindUnavailable, conductor.ErrUnreservedDispatch,
		"fleet dispatch: no fleet reservation runtime is bound")
	// errReadmitUnbound refuses a fresh re-admit: a re-entered job holding no
	// live reservation waits at leased for a normal admission.
	errReadmitUnbound = cascade.New(cascade.KindUnavailable,
		"fleet dispatch: a re-entered job without a live reservation waits for admission")
)

// wireFleetDispatch binds the dispatch slot and the scheduler seam.
func wireFleetDispatch(w *daemonWiring) error {
	fleet := w.Deps.Fleet
	if fleet == nil || fleet.Reserver() == nil || fleet.Fleet() == nil {
		w.Manifest.SetSchedulerReservation(refuseReadmit, refuseReadmit)
		return applyReadmitSchemaAt(w.Ctx, w.Paths, w.Clock)
	}
	// The scheduler's re-admit reads the delegation table; the delegate
	// wiring applies it only with a config, so it is applied here too.
	if err := jobs.ApplyDelegationSchema(w.Ctx, fleet.JobsDB(), migrate.SQLiteEmitter{}, w.Clock, "", ""); err != nil {
		return err
	}
	parts := fleet.CapacityParts()
	rows, _ := parts.Deps.Reservations.(reservationRows)
	dispatchSlotFor(w.Manifest).bind(&fleetDispatch{reserver: fleet.Reserver(), rows: rows, domainOf: parts.Deps.DomainOf,
		projects: &projectTracker{count: fleet.Projects(), live: map[string]int{}}, counter: provider.NaiveTokenCounter{}})
	w.Manifest.SetSchedulerReservation(refuseReadmit, fleet.Reserver().Adopt)
	return nil
}

// refuseReadmit is the re-admit seam half that has nothing to reserve with.
func refuseReadmit(context.Context, string) error { return errReadmitUnbound }

// applyReadmitSchemaAt applies the delegation schema to cascade.db over a
// short-lived handle, for a wiring without a fleet runtime. A nil paths (a
// bare wiring test) has no database to prepare.
func applyReadmitSchemaAt(ctx context.Context, paths runtime.PathProvider, clock runtime.Clock) error {
	if paths == nil {
		return nil
	}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "fleet dispatch: create data dir")
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(paths.DataDir(), "cascade.db")+"?_busy_timeout=5000")
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "fleet dispatch: open cascade.db")
	}
	defer func() { _ = db.Close() }()
	return jobs.ApplyDelegationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", "")
}

// dispatchSlots maps a daemon's Manifest to its dispatch slot.
var dispatchSlots sync.Map

// dispatchSlot is the CallReserver the Executor holds. It forwards to the
// bound fleet adapter and refuses every call until one is bound.
type dispatchSlot struct{ bound atomic.Pointer[fleetDispatch] }

// dispatchSlotFor returns m's slot, creating it on first use.
func dispatchSlotFor(m *daemon.Manifest) *dispatchSlot {
	v, _ := dispatchSlots.LoadOrStore(m, &dispatchSlot{})
	s, _ := v.(*dispatchSlot)
	return s
}

// bind installs the fleet adapter every later dispatch reserves through.
func (s *dispatchSlot) bind(d *fleetDispatch) { s.bound.Store(d) }

// Reserve forwards to the bound adapter.
func (s *dispatchSlot) Reserve(ctx context.Context, sel provider.Selection, req provider.ModelRequest) (conductor.HeldCall, error) {
	d := s.bound.Load()
	if d == nil {
		return nil, errDispatchUnbound
	}
	return d.Reserve(ctx, sel, req)
}

// Check forwards to the bound adapter.
func (s *dispatchSlot) Check(ctx context.Context, id string, jobID string, sel provider.Selection) (conductor.HeldCall, error) {
	d := s.bound.Load()
	if d == nil {
		return nil, errDispatchUnbound
	}
	return d.Check(ctx, id, jobID, sel)
}

// reservationRows reads one ledger row by id (the runtime's one store).
type reservationRows interface {
	Get(ctx context.Context, id string) (economics.Reservation, bool, error)
}

// fleetDispatch adapts the fleet Reserver to conductor.CallReserver.
type fleetDispatch struct {
	reserver *economics.Reserver
	rows     reservationRows
	domainOf func(context.Context, conductor.LaneID) (string, error)
	projects *projectTracker
	counter  provider.TokenCounter
}

// Reserve takes one quota-only interactive row on sel's quota domain.
func (d *fleetDispatch) Reserve(ctx context.Context, sel provider.Selection, req provider.ModelRequest) (conductor.HeldCall, error) {
	est, err := d.estimate(ctx, req)
	if err != nil {
		return nil, err
	}
	domain, err := d.domainOf(ctx, conductor.LaneID(sel.LaneID))
	if err != nil {
		return nil, err
	}
	exec, err := cascade.NewID()
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "fleet dispatch: mint execution id")
	}
	project := dispatchProject
	if req.TaskID != "" {
		project = req.TaskID
	}
	d.projects.start(project)
	r, err := d.reserver.Reserve(ctx, economics.ReserveRequest{ExecutionID: exec.String(), JobID: req.TaskID, ProjectID: project,
		LaneID: sel.LaneID, DomainID: domain, Kind: economics.ReservationInteractive, Estimate: est})
	if err == nil && r.State != economics.ReservationHeld {
		err = cascade.Newf(cascade.KindUnavailable, "fleet dispatch: reservation %q is %s, not held", r.ID, r.State)
		if _, rerr := d.reserver.Release(context.WithoutCancel(ctx), r.ID); rerr != nil {
			err = cascade.Wrapf(cascade.KindUnavailable, rerr, "fleet dispatch: reservation %q is %s and could not be released", r.ID, r.State)
		}
	}
	if err != nil {
		d.projects.stop(project)
		return nil, err
	}
	return &fleetHold{d: d, id: r.ID, project: project, owned: true}, nil
}

// estimate is EstimateFor over the summed input token count, one request.
func (d *fleetDispatch) estimate(ctx context.Context, req provider.ModelRequest) (economics.Estimate, error) {
	var n int64
	for _, m := range req.Inputs {
		c, err := d.counter.Count(ctx, m.Content)
		if err != nil {
			return economics.Estimate{}, cascade.Wrap(cascade.KindUnavailable, err, "fleet dispatch: counting input tokens")
		}
		n += int64(c)
	}
	return economics.EstimateFor(n, conductor.TaskClass(req.TaskClass), 1)
}

// Check resolves a caller's reservation: it must exist, be live, belong to
// jobID and sit on sel's lane. The caller keeps ownership of the row.
func (d *fleetDispatch) Check(ctx context.Context, id string, jobID string, sel provider.Selection) (conductor.HeldCall, error) {
	if d.rows == nil {
		return nil, cascade.New(cascade.KindUnavailable, "fleet dispatch: no reservation store to check against")
	}
	r, found, err := d.rows.Get(ctx, id)
	switch {
	case err != nil:
		return nil, err
	case !found:
		return nil, cascade.Newf(cascade.KindNotFound, "fleet dispatch: reservation %q not found", id)
	case r.State.Terminal():
		return nil, cascade.Newf(cascade.KindConflict, "fleet dispatch: reservation %q is %s", id, r.State)
	case r.JobID != jobID:
		return nil, cascade.Newf(cascade.KindPolicyDenied, "fleet dispatch: reservation %q belongs to another job", id)
	case r.LaneID != sel.LaneID:
		return nil, cascade.Newf(cascade.KindPolicyDenied, "fleet dispatch: reservation %q is on another lane", id)
	}
	return &fleetHold{d: d, id: r.ID}, nil
}

// fleetHold is one reservation a dispatch runs under. An owned hold was
// taken by Reserve and is released by the door; a checked one is not.
type fleetHold struct {
	d       *fleetDispatch
	id      string
	project string
	owned   bool
}

// ID returns the reservation id.
func (h *fleetHold) ID() string { return h.id }

// WithPermit runs fn under the Reserver's one per-call permit. A permit the
// governor refused is named for its cause; fn's own error passes through.
func (h *fleetHold) WithPermit(ctx context.Context, fn func(context.Context) error) error {
	ran := false
	err := h.d.reserver.WithPermit(ctx, h.id, func(c context.Context) error {
		ran = true
		return fn(c)
	})
	if err == nil || ran {
		return err
	}
	if governor.IsDraining(err) {
		return cascade.Wrap(cascade.KindUnavailable, err, "fleet dispatch: permit refused: node draining")
	}
	if governor.IsSignalRefusal(err) {
		return cascade.Wrap(cascade.KindUnavailable, err, "fleet dispatch: permit refused: no fresh resource signal")
	}
	return err
}

// Release reconciles the reported usage (or keeps the estimate) and
// releases an owned row; the project stops with its last live row.
func (h *fleetHold) Release(ctx context.Context, u provider.Usage, reported bool) error {
	if !h.owned {
		return nil
	}
	defer h.d.projects.stop(h.project)
	a, src := economics.Actual{}, economics.ActualSourceEstimated
	if reported {
		a = economics.Actual{TokensIn: int64(u.InputTokens), TokensOut: int64(u.OutputTokens), Requests: 1}
		src = economics.ActualSourceReported
	}
	_, cerr := h.d.reserver.Reconcile(ctx, h.id, a, src)
	_, rerr := h.d.reserver.Release(ctx, h.id)
	if rerr != nil && cerr != nil {
		return cascade.Wrapf(cascade.KindInternal, rerr, "fleet dispatch: release failed after reconcile failed (%v)", cerr)
	}
	if rerr != nil {
		return rerr
	}
	return cerr
}

// projectTracker drives the Reserver's active project count: a project
// starts with its first live row and stops with its last.
type projectTracker struct {
	mu    sync.Mutex
	count *economics.ActiveProjectCount
	live  map[string]int
}

// start counts one more live row for project.
func (p *projectTracker) start(project string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.live[project]++
	if p.live[project] == 1 {
		p.count.ProjectStarted()
	}
}

// stop counts one fewer live row for project.
func (p *projectTracker) stop(project string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch n := p.live[project]; {
	case n > 1:
		p.live[project] = n - 1
	case n == 1:
		delete(p.live, project)
		p.count.ProjectStopped()
	}
}
