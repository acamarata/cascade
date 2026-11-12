package delegation

// Purpose: startup recovery's refusals: missing dependencies, an unreadable
// effect log, a lease that does not match its record, and fleet failures.

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
	sqlite "github.com/acamarata/cascade/providers/sqlite"
)

// faultFleet fails Release and Abort with err.
type faultFleet struct{ err error }

func (f faultFleet) Abort(context.Context, capacity.Lease) error   { return f.err }
func (f faultFleet) Release(context.Context, capacity.Lease) error { return f.err }

// recoveryFaultDeps returns deps whose lease resolver yields lease id; sweep
// and worktree release succeed.
func recoveryFaultDeps(fleet faultFleet, id string, committed bool) RecoveryDeps {
	return RecoveryDeps{Fleet: fleet,
		ResolveLease: func(context.Context, jobs.DelegationRecord) (capacity.Lease, bool, error) {
			return capacity.Lease{ID: id}, committed, nil
		},
		ReleaseWorktree: func(context.Context, jobs.DelegationRecord) error { return nil },
		sweepLease:      func(context.Context, string) error { return nil }}
}

// TestRecoverPendingSpawnsRefusals: no dependencies is invalid input; a
// closed effect log fails before any record is touched.
func TestRecoverPendingSpawnsRefusals(t *testing.T) {
	ctx := context.Background()
	if err := RecoverPendingSpawns(ctx, RecoveryDeps{}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("no deps = %v, want KindInvalidInput", err)
	}
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "effects.db"))
	if err != nil {
		t.Fatal(err)
	}
	effects, err := audit.NewEffectLog(audit.New(store, fakeClock{now: t0}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	d := recoveryFaultDeps(faultFleet{}, "", false)
	d.Effects, d.Store = effects, new(jobs.Store)
	if err := RecoverPendingSpawns(ctx, d); err == nil {
		t.Fatal("recovery over a closed effect log succeeded")
	}
}

// TestRecoverDelegationLeaseAndFleetFaults: a resolved lease that differs
// from the record is an integrity error; Release (committed) and Abort
// (uncommitted) failures are returned unchanged.
func TestRecoverDelegationLeaseAndFleetFaults(t *testing.T) {
	ctx := context.Background()
	rec := jobs.DelegationRecord{ID: "d1", LeaseID: "lease-a"}
	err := recoverDelegation(ctx, recoveryFaultDeps(faultFleet{}, "lease-b", true), rec)
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("lease mismatch = %v, want KindIntegrity", err)
	}
	boom := errors.New("fleet down")
	for _, committed := range []bool{true, false} {
		err := recoverDelegation(ctx, recoveryFaultDeps(faultFleet{err: boom}, "lease-a", committed), rec)
		if !errors.Is(err, boom) {
			t.Errorf("committed=%v: %v, want the fleet error", committed, err)
		}
	}
}

// goBinary finds the go command on PATH (runtime.GOROOT is deprecated).
func goBinary(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go command not on PATH")
	}
	return path
}
