// Purpose: a lease committed before a restart is rebuilt from the ids stored
// with its delegation record and released through the fleet after recovery.

package delegation

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ledgerFleet stands in for the fleet after a restart: its in-memory lease
// index is empty, so it knows a lease only through the persisted reservations.
type ledgerFleet struct {
	fleetDouble
	rsv      map[string]economics.Reservation // by execution id
	released []capacity.Lease
}

func (l *ledgerFleet) Lease(_ context.Context, _ capacity.ResourceRequest, d capacity.DispatchRef, _ int) (capacity.Lease, error) {
	l.refs = append(l.refs, d)
	l.rsv["exec-1"] = economics.Reservation{ID: "rsv-1", ExecutionID: "exec-1", State: economics.ReservationHeld}
	return capacity.Lease{ID: "lease-1", ExecutionID: "exec-1", ReservationID: "rsv-1", LaneID: conductor.LaneID("agent:drv-1")}, nil
}

func (l *ledgerFleet) Commit(_ context.Context, lease capacity.Lease) error {
	r := l.rsv[lease.ExecutionID]
	r.State = economics.ReservationCommitted
	l.rsv[lease.ExecutionID] = r
	return l.log("commit")
}

func (l *ledgerFleet) Release(_ context.Context, lease capacity.Lease) error {
	l.released = append(l.released, lease)
	return l.log("release")
}

func (l *ledgerFleet) ReservationByExecution(_ context.Context, id string) (economics.Reservation, bool, error) {
	r, ok := l.rsv[id]
	return r, ok, nil
}

func TestRecoverRebuildsCommittedLease(t *testing.T) {
	ctx := context.Background()
	c, good, svc, _, _ := setup(t)
	before := &ledgerFleet{rsv: map[string]economics.Reservation{}}
	svc.deps.fleet = before
	resp, err := svc.Delegate(ctx, &Request{AgentID: "a", Parent: good})
	failIf(t, err != nil || resp == nil, "Delegate = %+v, %v", resp, err)
	rec, err := c.st.GetDelegation(ctx, resp.Record.Env.ID)
	failIf(t, err != nil || rec.ExecutionID != "exec-1" || rec.ReservationID != "rsv-1" || rec.LeaseID != "lease-1",
		"stored record = %+v, %v; want the lease's execution and reservation ids", rec, err)

	// Restart: a fresh fleet whose memory holds nothing but the ledger.
	after := &ledgerFleet{rsv: before.rsv}
	d := RecoveryDeps{Effects: recoveryEffects(t, filepath.Join(t.TempDir(), "effects.db")), Store: c.st, Fleet: after, ResolveLease: StoredLease(after),
		ReleaseWorktree: func(context.Context, jobs.DelegationRecord) error { return nil },
		sweepLease:      func(context.Context, string) error { return nil }}
	failIf(t, RecoverPendingSpawns(ctx, d) != nil, "recovery failed")
	want := []capacity.Lease{{ID: "lease-1", ExecutionID: "exec-1", ReservationID: "rsv-1", LaneID: "agent:drv-1"}}
	failIf(t, !reflect.DeepEqual(after.released, want) || !reflect.DeepEqual(after.calls, []string{"release"}),
		"released %+v (calls %v); want the committed lease released once", after.released, after.calls)
	rec, err = c.st.GetDelegation(ctx, rec.ID)
	failIf(t, err != nil || rec.State != jobs.DelegationFailed || rec.Reason != "daemon restart", "record = %+v, %v; want failed by the restart", rec, err)

	// A lease stored without its ids cannot be rebuilt: refused, never guessed.
	_, _, err = StoredLease(after)(ctx, jobs.DelegationRecord{ID: "old", LeaseID: "lease-0"})
	failIf(t, !cascade.HasKind(err, cascade.KindUnavailable), "legacy record: %v; want KindUnavailable", err)
	_, _, err = StoredLease(after)(ctx, jobs.DelegationRecord{ID: "x", LeaseID: "l", ExecutionID: "exec-1", ReservationID: "other"})
	failIf(t, !cascade.HasKind(err, cascade.KindIntegrity), "mismatched reservation: %v; want KindIntegrity", err)
}
