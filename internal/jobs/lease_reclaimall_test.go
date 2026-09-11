package jobs

// Purpose: closes the coverage gap on lease_fence.go's ReclaimAll,
//   which had no test at all: proves it reclaims what it should (a
//   dead-pgid expired_unconfirmed lease) AND leaves alone what it
//   should not (a live-pgid expired_unconfirmed lease, and a lease
//   never swept into expired_unconfirmed in the first place) -- a
//   reclaim-everything implementation would pass a test that only
//   checked the dead-pgid half.
// Inputs: nothing external.
// Outputs: n/a (test file).
// Constraints: elapsed time drives *runtime.FixedClock.Advance only.
//   Split into a setup helper + the assertion body to stay under the
//   50-line function cap.
// SPORT: jobs/lease-model (FIX, coverage floor restoration).

import (
	"context"
	"testing"
)

// pgidMapProbe answers IsAlive per-pgid, unlike fakeLivenessProbe (a
// single bool for every pgid), so ONE ReclaimAll call can present a mix
// of live and dead holders within the same sweep.
type pgidMapProbe map[int64]bool

func (p pgidMapProbe) IsAlive(pgid int64) bool { return p[pgid] }

// reclaimAllFixture is the three leases TestLeaseReclaimAllMixedOutcomes
// exercises: a dead-pgid one (should be reclaimed), a live-pgid one
// (should be left alone), and a never-swept held one (also left alone).
type reclaimAllFixture struct {
	m          *LeaseManager
	store      *Store
	deadGrant  AcquireResult
	aliveGrant AcquireResult
	heldGrant  AcquireResult
}

// setupReclaimAllFixture acquires the dead and alive leases, sweeps
// them both to expired_unconfirmed, THEN acquires the held lease at the
// later clock instant so it was never swept at all.
func setupReclaimAllFixture(t *testing.T) reclaimAllFixture {
	t.Helper()
	m, clock, store := newTestLeaseManager(t)
	ctx := context.Background()

	deadGrant, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-dead")
	if err != nil || !deadGrant.Granted {
		t.Fatalf("Acquire (dead): %+v, %v", deadGrant, err)
	}
	if err := store.PutJob(ctx, baseJob("job-dead")); err != nil {
		t.Fatalf("PutJob (dead): %v", err)
	}
	if err := store.PutExecution(ctx, Execution{ID: "exec-dead", JobID: "job-dead", Attempt: 1, State: ExecutionRunning, PGID: 1111}); err != nil {
		t.Fatalf("PutExecution (dead): %v", err)
	}

	aliveGrant, err := m.Acquire(ctx, "repo-1", "internal/nodes/**", "job-alive")
	if err != nil || !aliveGrant.Granted {
		t.Fatalf("Acquire (alive): %+v, %v", aliveGrant, err)
	}
	if err := store.PutJob(ctx, baseJob("job-alive")); err != nil {
		t.Fatalf("PutJob (alive): %v", err)
	}
	if err := store.PutExecution(ctx, Execution{ID: "exec-alive", JobID: "job-alive", Attempt: 1, State: ExecutionRunning, PGID: 2222}); err != nil {
		t.Fatalf("PutExecution (alive): %v", err)
	}

	deadline := deadGrant.Lease.IssuedAt + deadGrant.Lease.TTLSeconds + m.defaults.ExpiryGraceSeconds
	clock.Advance(secondsUntil(clock, deadline+1))
	if _, err := m.SweepExpired(ctx); err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}

	// Acquired only AFTER the sweep, at the later clock instant: this
	// lease must never have been swept, so ReclaimAll (which only ever
	// selects expired_unconfirmed rows) must leave it exactly as held.
	heldGrant, err := m.Acquire(ctx, "repo-2", "internal/fleet/**", "job-held")
	if err != nil || !heldGrant.Granted {
		t.Fatalf("Acquire (held): %+v, %v", heldGrant, err)
	}

	return reclaimAllFixture{m: m, store: store, deadGrant: deadGrant, aliveGrant: aliveGrant, heldGrant: heldGrant}
}

func TestLeaseReclaimAllMixedOutcomes(t *testing.T) {
	f := setupReclaimAllFixture(t)
	ctx := context.Background()

	probe := pgidMapProbe{1111: false, 2222: true}
	results, err := f.m.ReclaimAll(ctx, probe)
	if err != nil {
		t.Fatalf("ReclaimAll: %v", err)
	}

	assertReclaimAllOutcomes(t, f, results)
}

// assertReclaimAllOutcomes checks the three leases' final persisted
// state AND the results slice ReclaimAll returned, split out of
// TestLeaseReclaimAllMixedOutcomes to stay under the 50-line cap.
func assertReclaimAllOutcomes(t *testing.T, f reclaimAllFixture, results []ResourceLease) {
	t.Helper()
	ctx := context.Background()

	dead, ok, err := f.store.GetLease(ctx, "repo-1", f.deadGrant.Lease.ScopeGlob)
	if err != nil || !ok {
		t.Fatalf("GetLease (dead): ok=%v err=%v", ok, err)
	}
	if dead.State != LeaseExpiredOrphaned {
		t.Fatalf("dead-pgid lease state = %v, want expired_orphaned (ReclaimAll should have reclaimed it)", dead.State)
	}
	if dead.Epoch <= f.deadGrant.Lease.Epoch {
		t.Errorf("dead-pgid lease epoch = %d, want advanced past %d", dead.Epoch, f.deadGrant.Lease.Epoch)
	}

	alive, ok, err := f.store.GetLease(ctx, "repo-1", f.aliveGrant.Lease.ScopeGlob)
	if err != nil || !ok {
		t.Fatalf("GetLease (alive): ok=%v err=%v", ok, err)
	}
	if alive.State != LeaseExpiredUnconfirmed {
		t.Fatalf("live-pgid lease state = %v, want unchanged expired_unconfirmed (ReclaimAll must leave a live holder alone)", alive.State)
	}
	if alive.Epoch != f.aliveGrant.Lease.Epoch {
		t.Errorf("live-pgid lease epoch changed: got %d, want unchanged %d", alive.Epoch, f.aliveGrant.Lease.Epoch)
	}

	held, ok, err := f.store.GetLease(ctx, "repo-2", f.heldGrant.Lease.ScopeGlob)
	if err != nil || !ok {
		t.Fatalf("GetLease (held): ok=%v err=%v", ok, err)
	}
	if held.State != LeaseHeld || held.Epoch != f.heldGrant.Lease.Epoch {
		t.Fatalf("never-expired held lease was touched by ReclaimAll: %+v", held)
	}

	var sawDeadOrphaned, sawAliveTouched bool
	for _, r := range results {
		if r.RepoID == "repo-1" && r.ScopeGlob == f.deadGrant.Lease.ScopeGlob && r.State == LeaseExpiredOrphaned {
			sawDeadOrphaned = true
		}
		if r.RepoID == "repo-1" && r.ScopeGlob == f.aliveGrant.Lease.ScopeGlob && r.State == LeaseExpiredOrphaned {
			sawAliveTouched = true
		}
	}
	if !sawDeadOrphaned {
		t.Fatalf("ReclaimAll results %+v do not report the dead-pgid lease as reclaimed", results)
	}
	if sawAliveTouched {
		t.Fatalf("ReclaimAll results %+v report the live-pgid lease as orphaned, want untouched", results)
	}
}
