//go:build !windows

package jobs

// Purpose: a lease whose holder process is SIGKILLed is recovered by the
//
//	liveness loop's two lease steps, SweepExpired then ReclaimAll with the
//	real platform probe, once its ttl passes on the injected clock; a live
//	holder is never reclaimed (AUD-043 item 2).
// Constraints: the holder is this test binary re-executed in its own process
//
//	group (Setpgid), blocked until the parent kills it.
//
// SPORT: jobs/lease-model (ADD, P1-CAP-10).

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// leaseHolderChildEnv marks the re-executed holder process.
const leaseHolderChildEnv = "JOBS_LEASE_HOLDER_CHILD"

// startHolder re-executes this test as a blocked holder in its own group.
func startHolder(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLeaseOfKilledHolderRecoveredByExpiry$")
	cmd.Env = append(os.Environ(), leaseHolderChildEnv+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start holder: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

// TestLeaseOfKilledHolderRecoveredByExpiry: alive, the expired lease stays
// expired_unconfirmed and blocks; after SIGKILL the next sweep pass moves it
// to expired_orphaned and a new Acquire of the scope is granted.
func TestLeaseOfKilledHolderRecoveredByExpiry(t *testing.T) {
	if os.Getenv(leaseHolderChildEnv) == "1" {
		time.Sleep(10 * time.Minute) // the parent kills this process
		return
	}
	m, clock, store := newTestLeaseManager(t)
	ctx, probe := context.Background(), NewProcessLivenessProbe()
	granted, err := m.Acquire(ctx, "repo-kill", "k/**", "job-a")
	if err != nil || !granted.Granted {
		t.Fatalf("Acquire: %+v, %v", granted, err)
	}
	holder := startHolder(t)
	if err := store.PutJob(ctx, baseJob("job-a")); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	exec := Execution{ID: "exec-kill", JobID: "job-a", Attempt: 1, State: ExecutionRunning, PGID: int64(holder.Process.Pid)}
	if err := store.PutExecution(ctx, exec); err != nil {
		t.Fatalf("PutExecution: %v", err)
	}
	clock.Advance(time.Duration(granted.Lease.TTLSeconds+m.defaults.ExpiryGraceSeconds+1) * time.Second)
	sweepPass(ctx, t, m, probe)
	assertLeasePinnedAt(t, m, store, granted.Lease.Epoch, LeaseExpiredUnconfirmed)
	if err := holder.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL holder: %v", err)
	}
	_ = holder.Wait() // reaped: its process group is gone
	sweepPass(ctx, t, m, probe)
	stored, _, _ := store.GetLease(ctx, "repo-kill", "k/**")
	if stored.State != LeaseExpiredOrphaned || stored.Epoch <= granted.Lease.Epoch {
		t.Fatalf("lease after the holder died = %+v, want expired_orphaned at a newer epoch", stored)
	}
	again, err := m.Acquire(ctx, "repo-kill", "k/**", "job-b")
	if err != nil || !again.Granted {
		t.Fatalf("Acquire after reclaim = %+v, %v; want granted", again, err)
	}
}

// sweepPass is the liveness loop's lease half: SweepExpired, ReclaimAll.
func sweepPass(ctx context.Context, t *testing.T, m *LeaseManager, probe ProcessLivenessProbe) {
	t.Helper()
	if _, err := m.SweepExpired(ctx); err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if _, err := m.ReclaimAll(ctx, probe); err != nil {
		t.Fatalf("ReclaimAll: %v", err)
	}
}

// assertLeasePinnedAt checks the stored lease state and epoch and that a
// second job's Acquire is refused.
func assertLeasePinnedAt(t *testing.T, m *LeaseManager, store *Store, epoch int64, state LeaseState) {
	t.Helper()
	stored, _, err := store.GetLease(context.Background(), "repo-kill", "k/**")
	if err != nil || stored.State != state || stored.Epoch != epoch {
		t.Fatalf("lease with a live holder = %+v, %v; want %s at epoch %d", stored, err, state, epoch)
	}
	if got, err := m.Acquire(context.Background(), "repo-kill", "k/**", "job-b"); err != nil || got.Granted {
		t.Fatalf("Acquire behind a live holder = %+v, %v; want refused", got, err)
	}
}
