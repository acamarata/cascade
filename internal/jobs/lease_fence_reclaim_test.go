package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// countingLivenessProbe is a ProcessLivenessProbe that always answers
// dead and counts its calls, so a test can prove Reclaim never consulted
// it on a path that must not fence.
type countingLivenessProbe struct{ calls int }

// IsAlive records the call and reports dead.
func (p *countingLivenessProbe) IsAlive(int64) bool {
	p.calls++
	return false
}

// newExpiredUnconfirmedLease builds a fresh manager whose job-a holds
// "internal/jobs/**" and has been swept to expired_unconfirmed. When
// recordExec is true, job-a gets one execution row carrying pgid. It
// returns the manager, its store and the lease as granted.
func newExpiredUnconfirmedLease(t *testing.T, recordExec bool, pgid int64) (*LeaseManager, *Store, ResourceLease) {
	t.Helper()
	m, clock, store := newTestLeaseManager(t)
	ctx := context.Background()
	granted, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-a")
	if err != nil || !granted.Granted {
		t.Fatalf("Acquire: %+v, %v", granted, err)
	}
	if err := store.PutJob(ctx, baseJob("job-a")); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	if recordExec {
		exec := Execution{ID: "exec-1", JobID: "job-a", Attempt: 1, State: ExecutionRunning, PGID: pgid}
		if err := store.PutExecution(ctx, exec); err != nil {
			t.Fatalf("PutExecution(pgid %d): %v", pgid, err)
		}
	}
	deadline := granted.Lease.IssuedAt + granted.Lease.TTLSeconds + m.defaults.ExpiryGraceSeconds
	clock.Advance(secondsUntil(clock, deadline+1))
	if _, err := m.SweepExpired(ctx); err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	return m, store, granted.Lease
}

// assertLeasePinned checks the stored lease is still expired_unconfirmed
// at wantEpoch and that job-b's Acquire stays queued behind it.
func assertLeasePinned(t *testing.T, m *LeaseManager, store *Store, wantEpoch int64) {
	t.Helper()
	ctx := context.Background()
	stored, ok, err := store.GetLease(ctx, "repo-1", "internal/jobs/**")
	if err != nil || !ok {
		t.Fatalf("GetLease: ok=%v, %v", ok, err)
	}
	if stored.State != LeaseExpiredUnconfirmed || stored.Epoch != wantEpoch {
		t.Errorf("stored lease = state %v epoch %d, want expired_unconfirmed at unchanged epoch %d",
			stored.State, stored.Epoch, wantEpoch)
	}
	blocked, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-b")
	if err != nil {
		t.Fatalf("Acquire job-b: %v", err)
	}
	if blocked.Granted {
		t.Errorf("Acquire job-b granted, want queued behind the pinned lease")
	}
}

// assertNilProbeErr checks err is ErrNilLivenessProbe by identity and by
// message, since errors.Is on a cascade error compares Kind only.
func assertNilProbeErr(t *testing.T, call string, err error) {
	t.Helper()
	if err != ErrNilLivenessProbe {
		t.Errorf("%s error = %v, want ErrNilLivenessProbe by identity", call, err)
		return
	}
	if !strings.Contains(err.Error(), "jobs: reclaim needs a liveness probe") {
		t.Errorf("%s error message = %q, want the nil-probe message", call, err.Error())
	}
	if !errors.Is(err, ErrNilLivenessProbe) {
		t.Errorf("%s error does not match ErrNilLivenessProbe under errors.Is", call)
	}
}

// TestLeaseReclaimNilProbeRefuses proves a nil probe is a wiring bug
// Reclaim and ReclaimAll refuse outright: both return
// ErrNilLivenessProbe, and the lease, whose holder has a recorded pgid,
// stays expired_unconfirmed at its epoch with the contender queued.
func TestLeaseReclaimNilProbeRefuses(t *testing.T) {
	m, store, lease := newExpiredUnconfirmedLease(t, true, 4242)
	ctx := context.Background()

	got, err := m.Reclaim(ctx, "repo-1", lease.ScopeGlob, nil)
	assertNilProbeErr(t, "Reclaim(nil probe)", err)
	if got != (ResourceLease{}) {
		t.Errorf("Reclaim(nil probe) lease = %+v, want the zero lease", got)
	}
	all, err := m.ReclaimAll(ctx, nil)
	assertNilProbeErr(t, "ReclaimAll(nil probe)", err)
	if all != nil {
		t.Errorf("ReclaimAll(nil probe) results = %+v, want nil", all)
	}
	assertLeasePinned(t, m, store, lease.Epoch)
}

// TestLeaseReclaimMissingPgidNeverFences proves a holder with no
// recorded pgid (no execution row, or the pgid 0 sentinel) is never
// fenced: Reclaim leaves the lease expired_unconfirmed at its epoch,
// never calls the probe, and the contender stays queued.
func TestLeaseReclaimMissingPgidNeverFences(t *testing.T) {
	cases := []struct {
		name       string
		recordExec bool
	}{
		{name: "no execution row", recordExec: false},
		{name: "pgid 0", recordExec: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, store, lease := newExpiredUnconfirmedLease(t, tc.recordExec, 0)
			probe := &countingLivenessProbe{}
			got, err := m.Reclaim(context.Background(), "repo-1", lease.ScopeGlob, probe)
			if err != nil {
				t.Fatalf("Reclaim: %v", err)
			}
			if got.State != LeaseExpiredUnconfirmed || got.Epoch != lease.Epoch {
				t.Errorf("Reclaim = state %v epoch %d, want expired_unconfirmed at unchanged epoch %d",
					got.State, got.Epoch, lease.Epoch)
			}
			if probe.calls != 0 {
				t.Errorf("probe called %d times, want 0 for a holder with no recorded pgid", probe.calls)
			}
			assertLeasePinned(t, m, store, lease.Epoch)
		})
	}
}

// TestLeaseReclaimAllNilProbeRefusesBeforeListing proves ReclaimAll refuses
// a nil probe itself, before it lists any lease: with no expired lease to
// reclaim, the inner Reclaim never runs, so only ReclaimAll's own guard
// can produce the refusal.
func TestLeaseReclaimAllNilProbeRefusesBeforeListing(t *testing.T) {
	m, _, _ := newTestLeaseManager(t)
	all, err := m.ReclaimAll(context.Background(), nil)
	assertNilProbeErr(t, "ReclaimAll(nil probe, no expired leases)", err)
	if all != nil {
		t.Errorf("ReclaimAll(nil probe) results = %+v, want nil", all)
	}
}
