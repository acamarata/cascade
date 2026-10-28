package daemon

// Purpose: the fleet runtime's liveness loop, driven by an injected ticker
//
//	and clock: heartbeat, ExpireStale (skipped only after a failed heartbeat
//	write), SweepExpired and ReclaimAll, every interval.
//
// SPORT: internal/daemon (ADD, P1-CAP-10).

import (
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/provider"
)

const testInterval = 15 * time.Minute

// seedRow inserts a reservation of kind/state owned by epoch, holding one
// fresh lease on repo for job, and returns the lease.
func seedRow(t *testing.T, g *resRig, id, epoch, repo, job string, state economics.ReservationState) jobs.ResourceLease {
	t.Helper()
	got, err := g.rt.Leases().Acquire(g.ctx, repo, "l/**", job)
	if err != nil || !got.Granted {
		t.Fatalf("Acquire(%s) = %+v, %v", repo, got, err)
	}
	r := economics.Reservation{ID: id, ExecutionID: "exec-" + id, JobID: job, RepoID: repo, ScopeGlobs: []string{"l/**"},
		Kind: economics.ReservationInteractive, State: state, OwnerEpoch: epoch, HeartbeatAt: g.clock.Now().Unix(),
		LeaseIDs: []string{leaseHandle(got.Lease)}}
	if _, err := g.rt.res.resStore.Insert(g.ctx, r); err != nil {
		t.Fatalf("Insert(%s): %v", id, err)
	}
	return got.Lease
}

// tickN advances the clock by one interval and delivers one tick, n times.
func (g *resRig) tickN(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		g.clock.Advance(testInterval)
		g.live.tick(t)
	}
}

// rowState reads a reservation's state.
func rowState(t *testing.T, g *resRig, id string) economics.Reservation {
	t.Helper()
	r, _, err := g.rt.res.resStore.Get(g.ctx, id)
	if err != nil {
		t.Fatalf("Get(%s): %v", id, err)
	}
	return r
}

// leaseState reads repo's l/** lease.
func leaseState(t *testing.T, g *resRig, repo string) jobs.ResourceLease {
	t.Helper()
	l, _, err := g.rt.Jobs().GetLease(g.ctx, repo, "l/**")
	if err != nil {
		t.Fatalf("GetLease(%s): %v", repo, err)
	}
	return l
}

// TestFleetRuntimeLivenessLoop: a dead epoch's committed row adopted by the
// start sweep is released after three intervals with its lease freed, and a
// lease whose holder process is gone is expired then reclaimed after its ttl.
func TestFleetRuntimeLivenessLoop(t *testing.T) {
	g := newResRig(t)
	seedRow(t, g, "res-adopted", "epoch-dead", "repo-adopted", "job-adopted", economics.ReservationCommitted)
	orphan, err := g.rt.Leases().Acquire(g.ctx, "repo-orphan", "l/**", "job-orphan")
	if err != nil || !orphan.Granted {
		t.Fatalf("Acquire orphan = %+v, %v", orphan, err)
	}
	if err := g.rt.Jobs().PutJob(g.ctx, jobs.Job{ID: "job-orphan", State: jobs.JobStatePending, ConsequenceClass: jobs.ConsequenceNormal,
		DataClass: new(provider.SensitivityInternal)}); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	if err := g.rt.Jobs().PutExecution(g.ctx, jobs.Execution{ID: "exec-orphan", JobID: "job-orphan", Attempt: 1, State: jobs.ExecutionRunning, PGID: 4242}); err != nil {
		t.Fatalf("PutExecution: %v", err)
	}
	g.startLoop(t)
	if r := rowState(t, g, "res-adopted"); r.OwnerEpoch != g.rt.OwnerEpoch() || r.State != economics.ReservationCommitted {
		t.Fatalf("after the start sweep = %+v, want adopted (committed, this epoch)", r)
	}
	g.tickN(t, 3)
	if r := rowState(t, g, "res-adopted"); r.State != economics.ReservationCommitted {
		t.Fatalf("after 3 intervals = %q, want still committed (heartbeat not yet older than 3 intervals)", r.State)
	}
	g.tickN(t, 1)
	if r := rowState(t, g, "res-adopted"); r.State != economics.ReservationReleased {
		t.Fatalf("after 4 intervals = %q, want released by ExpireStale", r.State)
	}
	if l := leaseState(t, g, "repo-adopted"); l.State != jobs.LeaseReleased {
		t.Fatalf("adopted row's lease = %+v, want released", l)
	}
	g.tickN(t, 5) // 135 minutes: past the orphan's ttl (2h) plus grace (10m)
	if l := leaseState(t, g, "repo-orphan"); l.State != jobs.LeaseExpiredOrphaned || l.Epoch <= orphan.Lease.Epoch {
		t.Fatalf("orphan lease = %+v, want expired_orphaned at a newer epoch (SweepExpired then ReclaimAll)", l)
	}
}

// TestLiveReservationLeaseSurvivesSweep: a reservation this runtime holds,
// whose job has no pgid, keeps its lease across 3 x ttl: Heartbeat renews
// it every interval, so SweepExpired never expires it and a second Acquire
// of the scope stays refused.
func TestLiveReservationLeaseSurvivesSweep(t *testing.T) {
	g := newResRig(t)
	g.startLoop(t)
	seedRow(t, g, "res-live", g.rt.OwnerEpoch(), "repo-live", "job-live", economics.ReservationHeld)
	if err := g.rt.Reserver().Adopt(g.ctx, "res-live"); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	g.tickN(t, 3*8) // 3 x ttl at 15-minute intervals
	l := leaseState(t, g, "repo-live")
	if l.State != jobs.LeaseHeld || l.RenewCount < 24 {
		t.Fatalf("live reservation's lease = %+v, want held and renewed every tick", l)
	}
	if got, err := g.rt.Leases().Acquire(g.ctx, "repo-live", "l/**", "job-other"); err != nil || got.Granted {
		t.Fatalf("second Acquire = %+v, %v; want refused", got, err)
	}
	if r := rowState(t, g, "res-live"); r.State != economics.ReservationHeld || r.HeartbeatAt != g.clock.Now().Unix() {
		t.Fatalf("live row = %+v, want held with this tick's heartbeat", r)
	}
}

// TestLivenessLoopSkipsExpireAfterHeartbeatError: a tick whose heartbeat
// write fails skips ExpireStale (the stale row survives it); a tick with a
// lost lease and no error does not skip it; the next good tick retires.
func TestLivenessLoopSkipsExpireAfterHeartbeatError(t *testing.T) {
	g := newResRig(t)
	g.startLoop(t)
	seedRow(t, g, "res-own", g.rt.OwnerEpoch(), "repo-own", "job-own", economics.ReservationHeld)
	if err := g.rt.Reserver().Adopt(g.ctx, "res-own"); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	g.clock.Advance(-4 * testInterval)
	seedRow(t, g, "res-stale", "epoch-dead", "repo-stale", "job-stale", economics.ReservationHeld)
	g.clock.Advance(4 * testInterval)
	exec := func(q string) {
		if _, err := g.rt.JobsDB().ExecContext(g.ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`CREATE TRIGGER fail_hb BEFORE UPDATE OF heartbeat_at ON jobs_reservation WHEN NEW.id = 'res-own' BEGIN SELECT RAISE(ABORT, 'injected heartbeat failure'); END`)
	g.live.tick(t)
	if r := rowState(t, g, "res-stale"); r.State != economics.ReservationHeld {
		t.Fatalf("stale row after a failed heartbeat = %q, want untouched (ExpireStale skipped)", r.State)
	}
	exec(`DROP TRIGGER fail_hb`)
	seedRow(t, g, "res-own2", g.rt.OwnerEpoch(), "repo-own2", "job-own2", economics.ReservationHeld)
	if err := g.rt.Reserver().Adopt(g.ctx, "res-own2"); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	// One tick with both: res-own's lease lost and res-own2's heartbeat write failing.
	exec(`CREATE TRIGGER fail_hb2 BEFORE UPDATE OF heartbeat_at ON jobs_reservation WHEN NEW.id = 'res-own2' BEGIN SELECT RAISE(ABORT, 'injected'); END`)
	exec(`UPDATE jobs_resource_lease SET epoch = epoch + 1 WHERE repo_id = 'repo-own'`)
	g.live.tick(t)
	if r := rowState(t, g, "res-own"); r.State != economics.ReservationRolledBack {
		t.Fatalf("own row with a lost lease = %q, want rolled back by Heartbeat", r.State)
	}
	if r := rowState(t, g, "res-stale"); r.State != economics.ReservationRolledBack {
		t.Fatalf("stale row on the lost-lease tick = %q, want rolled back (ExpireStale ran)", r.State)
	}
	exec(`DROP TRIGGER fail_hb2`)
	g.clock.Advance(-4 * testInterval)
	seedRow(t, g, "res-stale2", "epoch-dead", "repo-stale2", "job-stale2", economics.ReservationHeld)
	g.clock.Advance(4 * testInterval)
	g.live.tick(t) // a clean tick: no heartbeat error, no lost lease
	if r := rowState(t, g, "res-stale2"); r.State != economics.ReservationRolledBack {
		t.Fatalf("stale row on a clean tick = %q, want rolled back", r.State)
	}
}

// TestStartSweepRowConflictDoesNotRefuseStart: a dead epoch's row whose
// teardown fails with KindConflict (a dirty worktree) fails the sweep row
// but not the start; the liveness loop still starts.
func TestStartSweepRowConflictDoesNotRefuseStart(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("the reservation runtime serves only the !windows daemon")
	}
	g := newResRig(t)
	repo := seedReserveTopology(t, g)
	r, err := g.rt.Reserver().Reserve(g.ctx, economics.ReserveRequest{ExecutionID: "exec-dirty", JobID: "job-dirty", ProjectID: "p", LaneID: "lane-1",
		DomainID: "dom-seams", RepoID: repo, Kind: economics.ReservationInteractive, BasePrice: 1,
		Estimate: economics.Estimate{TokensIn: 1, TokensOut: 1, Requests: 1}, ScopeGlobs: []string{"src/**"}})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := os.WriteFile(filepath.Join(r.WorktreeID, "dirty.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("dirty the worktree: %v", err)
	}
	if _, err := g.rt.JobsDB().ExecContext(g.ctx, `UPDATE jobs_reservation SET owner_epoch = 'epoch-dead' WHERE id = ?`, r.ID); err != nil {
		t.Fatalf("age the row: %v", err)
	}
	g.startLoop(t)
	if row := subsystemRow(g.deps.Manifest, fleetReservationSweepSubsystem); row.State != SubsystemError {
		t.Fatalf("sweep row = %+v, want failed with the row's error", row)
	}
}

// TestStartSweepRefusesWhenRowsUnreadable: a sweep that cannot list the
// rows refuses the start (it never reached them).
func TestStartSweepRefusesWhenRowsUnreadable(t *testing.T) {
	g := newResRig(t)
	if _, err := g.rt.JobsDB().ExecContext(g.ctx, `ALTER TABLE jobs_reservation RENAME TO jobs_reservation_gone`); err != nil {
		t.Fatalf("rename: %v", err)
	}
	err := g.rt.StartReservations(g.ctx, fakeProber{})
	if err == nil {
		t.Fatalf("StartReservations over an unreadable ledger = %v, want a refusal", err)
	}
	if again := g.rt.StartReservations(g.ctx, fakeProber{}); again == nil {
		t.Fatal("a second StartReservations was accepted, want a refusal")
	}
}
