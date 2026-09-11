package jobs

import (
	"context"
	"testing"
)

// fakeLivenessProbe is a deterministic ProcessLivenessProbe for tests:
// alive reports IsAlive's answer for every pgid, so a test can flip a
// resuming holder from "still running" to "confirmed dead" without any
// real process.
type fakeLivenessProbe struct{ alive bool }

func (p fakeLivenessProbe) IsAlive(int64) bool { return p.alive }

// TestLeaseFenceMismatchRefused proves the single fence validation
// entry point: a presented epoch that does not match the stored row's
// current epoch is refused with ErrLeaseFenced -- the actual mismatch
// is constructed here, not inferred from a passing case.
func TestLeaseFenceMismatchRefused(t *testing.T) {
	m, _, _ := newTestLeaseManager(t)
	ctx := context.Background()
	granted, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-a")
	if err != nil || !granted.Granted {
		t.Fatalf("Acquire: %+v, %v", granted, err)
	}
	if err := m.Fence(ctx, "repo-1", granted.Lease.ScopeGlob, granted.Lease.Epoch+1); err == nil {
		t.Fatal("Fence at a stale epoch = nil error, want ErrLeaseFenced")
	}
	// Prove the positive path is not vacuously satisfied by Fence
	// always refusing: the CORRECT epoch must succeed.
	if err := m.Fence(ctx, "repo-1", granted.Lease.ScopeGlob, granted.Lease.Epoch); err != nil {
		t.Fatalf("Fence at the current epoch: %v", err)
	}
}

// TestLeaseFenceUnknownLeaseRefused proves Fence refuses a scope with no
// lease row at all, rather than treating "not found" as a permissive
// match.
func TestLeaseFenceUnknownLeaseRefused(t *testing.T) {
	m, _, _ := newTestLeaseManager(t)
	ctx := context.Background()
	if err := m.Fence(ctx, "repo-1", "internal/jobs", 1); err == nil {
		t.Fatal("Fence on an unheld scope = nil error, want ErrLeaseFenced")
	}
}

// TestLeaseReclaimLivePgidNeverPreempted constructs a resuming holder
// whose recorded pgid IS still alive, and asserts Reclaim leaves the row
// exactly as expired_unconfirmed -- the contender must stay queued.
func TestLeaseReclaimLivePgidNeverPreempted(t *testing.T) {
	m, clock, store := newTestLeaseManager(t)
	ctx := context.Background()
	granted, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-a")
	if err != nil || !granted.Granted {
		t.Fatalf("Acquire: %+v, %v", granted, err)
	}
	if err := store.PutJob(ctx, baseJob("job-a")); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	if err := store.PutExecution(ctx, Execution{ID: "exec-1", JobID: "job-a", Attempt: 1, State: ExecutionRunning, PGID: 4242}); err != nil {
		t.Fatalf("PutExecution: %v", err)
	}
	deadline := granted.Lease.IssuedAt + granted.Lease.TTLSeconds + m.defaults.ExpiryGraceSeconds
	clock.Advance(secondsUntil(clock, deadline+1))
	if _, err := m.SweepExpired(ctx); err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}

	reclaimed, err := m.Reclaim(ctx, "repo-1", granted.Lease.ScopeGlob, fakeLivenessProbe{alive: true})
	if err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	if reclaimed.State != LeaseExpiredUnconfirmed {
		t.Fatalf("Reclaim with a live pgid = state %v, want unchanged expired_unconfirmed", reclaimed.State)
	}
	if reclaimed.Epoch != granted.Lease.Epoch {
		t.Errorf("Reclaim with a live pgid advanced the epoch: got %d, want unchanged %d", reclaimed.Epoch, granted.Lease.Epoch)
	}
	blocked, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-b")
	if err != nil {
		t.Fatalf("Acquire against a live-pgid expired_unconfirmed lease: %v", err)
	}
	if blocked.Granted {
		t.Fatal("Acquire succeeded against a live-pgid expired_unconfirmed lease -- a live holder was preempted")
	}
}

// TestLeaseReclaimDeadPgidAdvancesEpoch is the confirmed-termination
// path: a dead pgid moves the row to expired_orphaned, advances the
// epoch, and a resuming job with the SAME holder id re-acquires at the
// new epoch.
func TestLeaseReclaimDeadPgidAdvancesEpoch(t *testing.T) {
	m, clock, store := newTestLeaseManager(t)
	ctx := context.Background()
	granted, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-a")
	if err != nil || !granted.Granted {
		t.Fatalf("Acquire: %+v, %v", granted, err)
	}
	if err := store.PutJob(ctx, baseJob("job-a")); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	if err := store.PutExecution(ctx, Execution{ID: "exec-1", JobID: "job-a", Attempt: 1, State: ExecutionRunning, PGID: 4242}); err != nil {
		t.Fatalf("PutExecution: %v", err)
	}
	deadline := granted.Lease.IssuedAt + granted.Lease.TTLSeconds + m.defaults.ExpiryGraceSeconds
	clock.Advance(secondsUntil(clock, deadline+1))
	if _, err := m.SweepExpired(ctx); err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}

	reclaimed, err := m.Reclaim(ctx, "repo-1", granted.Lease.ScopeGlob, fakeLivenessProbe{alive: false})
	if err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	if reclaimed.State != LeaseExpiredOrphaned {
		t.Fatalf("Reclaim with a dead pgid = state %v, want expired_orphaned", reclaimed.State)
	}
	if reclaimed.Epoch <= granted.Lease.Epoch {
		t.Errorf("Reclaim with a dead pgid did not advance the epoch: got %d, want > %d", reclaimed.Epoch, granted.Lease.Epoch)
	}

	resumed, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-a") // same holder id resumes
	if err != nil || !resumed.Granted {
		t.Fatalf("resuming Acquire after reclaim = %+v, err=%v, want granted", resumed, err)
	}
	if resumed.Lease.Epoch <= reclaimed.Epoch {
		t.Errorf("resumed Acquire epoch = %d, want strictly greater than reclaim's %d", resumed.Lease.Epoch, reclaimed.Epoch)
	}
}

// TestLeaseReclaimNoOpOnNonExpiredLease proves Reclaim is a no-op
// against a held (never expired) lease -- the reclaim path must never
// touch a live grant.
func TestLeaseReclaimNoOpOnNonExpiredLease(t *testing.T) {
	m, _, _ := newTestLeaseManager(t)
	ctx := context.Background()
	granted, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-a")
	if err != nil || !granted.Granted {
		t.Fatalf("Acquire: %+v, %v", granted, err)
	}
	result, err := m.Reclaim(ctx, "repo-1", granted.Lease.ScopeGlob, fakeLivenessProbe{alive: false})
	if err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	if result.State != LeaseHeld || result.Epoch != granted.Lease.Epoch {
		t.Errorf("Reclaim touched a held lease: %+v", result)
	}
}
