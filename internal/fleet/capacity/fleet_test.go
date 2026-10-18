// Purpose: the shared Fleet fixture (a real SQLite database holding the jobs
// schema, the reservation schema and a test decision table, a real
// economics.Reserver with recording lease and worktree seams, a snapshot with
// one entitled lane per tier) plus the construction and happy-path tests.
//
// SPORT: fleet.capacity.lease.

package capacity

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver for these tests

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const testControllerNode = "node-controller"

// callLog is a race-safe ordered log of the lease and worktree seam calls.
type callLog struct {
	mu   sync.Mutex
	logs []string
}

func (c *callLog) add(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = append(c.logs, s)
}

func (c *callLog) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.logs...)
}

// sqlDecisions is the in-package DecisionRecorder: it writes test_decision
// through the Bind transaction and can be told to fail or to run a hook.
type sqlDecisions struct {
	mu       sync.Mutex
	err      error
	hook     func()
	recorded []DecisionRecord
}

func (r *sqlDecisions) Record(ctx context.Context, q DecisionExecer, d DecisionRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	_, err := q.ExecContext(ctx, `INSERT INTO test_decision (id, job_id, execution_id, tier, lane, node, score, jump_fired, jump_code, reserve) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		d.ID, d.JobID, d.ExecutionID, string(d.SelectedTier), string(d.LaneID), d.NodeID, d.ScoreAtSelection, d.JumpRuleFired, string(d.JumpReasonCode), d.ReserveTier0)
	if err == nil {
		r.recorded = append(r.recorded, d)
		if r.hook != nil {
			r.hook()
		}
	}
	return err
}

// fx is one Fleet over a real database.
type fx struct {
	t         *testing.T
	db        *sql.DB
	jobs      *jobs.Store
	store     *economics.ReservationStore
	reserver  *economics.Reserver
	clock     *fakeClock
	calls     *callLog
	decisions *sqlDecisions
	snap      FleetSnapshot
	scores    stubScorer
	book      *leaseBook
	// onAcquire runs inside AcquireLeases (a test parks the in-flight row there).
	onAcquire func(context.Context)
	// releaseErr and removeErr fail the lease release and the worktree removal.
	releaseErr, removeErr error
	deps                  FleetDeps
	fleet                 *Fleet
}

func entitled() provider.CompliancePosture {
	return provider.CompliancePosture{ProgrammaticEntitlement: true}
}

// testSnapshot has one entitled lane per tier: two external, tier-0 local.
func testSnapshot(now time.Time) FleetSnapshot {
	mk := func(id string, tier Tier, lt conductor.LaneType) ProviderSlot {
		return ProviderSlot{LaneID: conductor.LaneID(id), Posture: entitled(), Tier: tier, LaneType: lt, ProfileRef: id, State: StateAvailable, UpdatedAt: now}
	}
	return FleetSnapshot{GeneratedAt: now, Lanes: map[conductor.LaneID]ProviderSlot{
		"lane-t2": mk("lane-t2", TierTwo, conductor.LaneExternalAPI),
		"lane-t1": mk("lane-t1", TierOne, conductor.LaneExternalAPI),
		"lane-t0": mk("lane-t0", TierZero, conductor.LaneControllerLocal),
	}}
}

func openFxDB(t *testing.T, clock *fakeClock) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "fleet-test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := jobs.ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyJobsSchema: %v", err)
	}
	if err := economics.ApplyReservationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyReservationSchema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE test_decision (id TEXT PRIMARY KEY, job_id TEXT, execution_id TEXT, tier TEXT, lane TEXT, node TEXT, score REAL, jump_fired INTEGER, jump_code TEXT, reserve INTEGER)`); err != nil {
		t.Fatalf("create test_decision: %v", err)
	}
	return db
}

// newFx builds the fixture and a Fleet over the default deps.
func newFx(t *testing.T) *fx {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	f := &fx{t: t, clock: clock, calls: &callLog{}, book: newLeaseBook(), decisions: &sqlDecisions{}, snap: testSnapshot(clock.t),
		scores: stubScorer{scores: map[Tier]float64{TierZero: 0.9, TierOne: 0.8, TierTwo: 0.85}}}
	f.db = openFxDB(t, clock)
	f.jobs = jobs.NewStore(f.db)
	var err error
	if f.store, err = economics.NewReservationStore(f.db, clock); err != nil {
		t.Fatalf("NewReservationStore: %v", err)
	}
	if f.reserver, err = economics.NewReserver(f.store, clock, "epoch-1", f.seams()); err != nil {
		t.Fatalf("NewReserver: %v", err)
	}
	policy := newTestPolicy(stubEstimator{coldDuration: time.Minute}, clock)
	f.deps = FleetDeps{
		Snapshot: func(context.Context) (FleetSnapshot, error) { return f.snap, nil },
		Selector: NewTierPolicySelector(policy), Scorer: func(string) CapabilityScorer { return f.scores },
		Reserver: f.reserver, Reservations: f.store, Jobs: f.jobs, Decisions: f.decisions,
		DomainOf:         func(context.Context, conductor.LaneID) (string, error) { return "domain-1", nil },
		Place:            func(context.Context, nodes.Requirement) (string, error) { return "node-worker", nil },
		ControllerNodeID: testControllerNode, Clock: clock, TTL: time.Minute,
	}
	f.rebuild()
	return f
}

// rebuild constructs the Fleet from f.deps.
func (f *fx) rebuild() {
	f.t.Helper()
	fl, err := NewFleet(f.deps)
	if err != nil {
		f.t.Fatalf("NewFleet: %v", err)
	}
	f.fleet = fl
}

func TestFleetLeaseTier2HappyPath(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	l, err := f.fleet.Lease(t.Context(), request(), dispatchFor("job-1"), 1)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if l.ID == "" || l.ReservationID == "" || l.ExecutionID == "" || l.DecisionID == "" {
		t.Fatalf("lease ids = %+v, want all four set", l)
	}
	if l.SelectedTier != TierTwo || l.LaneID != "lane-t2" || l.Sensitivity != provider.SensitivityInternal || l.NodeID != testControllerNode {
		t.Fatalf("lease = %+v, want tier-2, lane-t2, internal, controller node", l)
	}
	r, ok, err := f.fleet.ReservationByExecution(t.Context(), l.ExecutionID)
	if err != nil || !ok {
		t.Fatalf("ReservationByExecution = (%v, %v)", ok, err)
	}
	if r.State != economics.ReservationHeld || r.ID != l.ReservationID || r.NodeID != l.NodeID || r.SelectedTier != "tier-2" ||
		r.Sensitivity != "internal" || r.DecisionID != l.DecisionID || r.LaneID != "lane-t2" || r.DomainID != "domain-1" {
		t.Fatalf("reservation = %+v, does not agree with lease %+v", r, l)
	}
	e, ok, err := f.jobs.GetExecution(t.Context(), l.ExecutionID)
	if err != nil || !ok || e.JobID != "job-1" || e.Attempt != 1 {
		t.Fatalf("execution = (%+v, %v, %v), want the job-1 attempt-1 row", e, ok, err)
	}
	if got := f.decisions.recorded; len(got) != 1 || got[0].ID != l.DecisionID || got[0].ExecutionID != l.ExecutionID {
		t.Fatalf("decisions = %+v, want one row naming the lease", got)
	}
	if l.TTL != time.Minute || !l.Issued.Equal(f.clock.t) {
		t.Fatalf("lease ttl/issued = %v/%v, want the injected TTL and clock", l.TTL, l.Issued)
	}
}

func TestNewFleetRefusesNilDeps(t *testing.T) {
	base := newFx(t).deps
	cases := map[string]func(*FleetDeps){
		"Snapshot": func(d *FleetDeps) { d.Snapshot = nil }, "Selector": func(d *FleetDeps) { d.Selector = nil },
		"Scorer": func(d *FleetDeps) { d.Scorer = nil }, "Reserver": func(d *FleetDeps) { d.Reserver = nil },
		"Reservations": func(d *FleetDeps) { d.Reservations = nil }, "Jobs": func(d *FleetDeps) { d.Jobs = nil },
		"Decisions": func(d *FleetDeps) { d.Decisions = nil }, "DomainOf": func(d *FleetDeps) { d.DomainOf = nil },
		"Place": func(d *FleetDeps) { d.Place = nil }, "ControllerNodeID": func(d *FleetDeps) { d.ControllerNodeID = "" },
		"Clock": func(d *FleetDeps) { d.Clock = nil }, "TTL": func(d *FleetDeps) { d.TTL = 0 },
		"Floors": func(d *FleetDeps) { d.Floors = []LaneFilter{nil} },
	}
	for name, mutate := range cases {
		d := base
		mutate(&d)
		_, err := NewFleet(d)
		if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(fmt.Sprint(err), "FleetDeps."+name) {
			t.Errorf("NewFleet(%s unset) = %v, want KindInvalidInput naming FleetDeps.%s", name, err, name)
		}
	}
	d := base
	d.Floors = nil
	if _, err := NewFleet(d); err != nil {
		t.Errorf("NewFleet(no Floors) = %v, want nil: Floors is optional", err)
	}
}

func TestFleetLeaseUsesGlobalScorerKey(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	var keys []string
	f.deps.Scorer = func(key string) CapabilityScorer { keys = append(keys, key); return f.scores }
	f.rebuild()
	for range 2 {
		if _, err := f.fleet.Lease(t.Context(), request(), dispatchFor("job-1"), 1); err != nil {
			t.Fatalf("Lease: %v", err)
		}
	}
	if len(keys) != 2 || keys[0] != "global" || keys[1] != "global" {
		t.Fatalf("scorer keys = %v, want [global global]", keys)
	}
}
