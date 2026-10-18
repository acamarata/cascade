// Purpose: Fleet.Lease and its companions against the one reservation ledger:
// atomic Bind (a failure at any step leaves nothing but a rolled-back row),
// one reservation insert per lease, idempotent Release and Abort with the
// reverse teardown, stale expiry and the read by execution id.
//
// SPORT: fleet.capacity.lease.

package capacity

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// scopedDispatch leases repo leases and a worktree, so teardown is visible.
func scopedDispatch(job string) DispatchRef {
	d := dispatchFor(job)
	d.RepoID, d.ScopeGlobs = "repo-a", []string{"pkg/**"}
	return d
}

// reverseTeardown is the call order of a scoped lease that was torn down.
var reverseTeardown = []string{"acquire_leases", "allocate_worktree", "remove_worktree", "release_leases"}

// mustState fails unless exactly one reservation is in state and none is held.
func (f *fx) mustState(state economics.ReservationState) economics.Reservation {
	f.t.Helper()
	rs, err := f.store.ListByState(context.Background(), state)
	if err != nil || len(rs) != 1 {
		f.t.Fatalf("ListByState(%s) = (%d rows, %v), want exactly one", state, len(rs), err)
	}
	if held, _ := f.store.ListByState(context.Background(), economics.ReservationHeld); len(held) != 0 {
		f.t.Fatalf("%d held rows remain, want none", len(held))
	}
	return rs[0]
}

func TestFleetLeaseReservationAtomic(t *testing.T) {
	failures := map[string]func(*fx) context.Context{
		"execution-row insert": func(f *fx) context.Context {
			f.exec(`CREATE TRIGGER fail_exec BEFORE INSERT ON jobs_execution BEGIN SELECT RAISE(ABORT, 'forced execution insert failure'); END`)
			return context.Background()
		},
		"decision record": func(f *fx) context.Context {
			f.decisions.err = errors.New("forced decision failure")
			return context.Background()
		},
		"placement write": func(f *fx) context.Context {
			f.exec(`CREATE TRIGGER fail_placement BEFORE UPDATE ON jobs_reservation WHEN NEW.node_id <> '' BEGIN SELECT RAISE(ABORT, 'forced placement failure'); END`)
			return context.Background()
		},
		"commit": func(f *fx) context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			f.decisions.hook = cancel
			return ctx
		},
	}
	for name, arm := range failures {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			f.seedJob("job-1", provider.SensitivityInternal)
			ctx := arm(f)
			_, err := f.fleet.Lease(ctx, request(), scopedDispatch("job-1"), 1)
			requireSentinel(t, err, ErrReservationFailed, "bind the placement of lane")
			if n := f.count("jobs_execution"); n != 0 {
				t.Errorf("jobs_execution has %d rows, want 0", n)
			}
			if n := f.count("test_decision"); n != 0 {
				t.Errorf("test_decision has %d rows, want 0", n)
			}
			if r := f.mustState(economics.ReservationRolledBack); r.NodeID != "" || r.DecisionID != "" {
				t.Errorf("rolled-back row kept placement %q/%q, want none", r.NodeID, r.DecisionID)
			}
			if got := f.calls.all(); !slices.Equal(got, reverseTeardown) {
				t.Errorf("calls = %v, want every acquired step compensated in reverse: %v", got, reverseTeardown)
			}
			if live, wts := f.book.liveCount(), f.book.worktreeCount(); live != 0 || wts != 0 {
				t.Errorf("compensation left %d subordinate leases and %d worktrees held, want none", live, wts)
			}
		})
	}
}

// exec runs raw SQL on the fixture database.
func (f *fx) exec(q string) {
	f.t.Helper()
	if _, err := f.db.ExecContext(context.Background(), q); err != nil {
		f.t.Fatalf("exec %q: %v", q, err)
	}
}

func TestOneReservationInsertPerLease(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	f.exec(`CREATE TABLE ins_counter (id TEXT NOT NULL UNIQUE)`)
	f.exec(`CREATE TRIGGER count_ins AFTER INSERT ON jobs_reservation BEGIN INSERT INTO ins_counter (id) VALUES (NEW.id); END`)
	first, err := f.fleet.Lease(t.Context(), request(), dispatchFor("job-1"), 1)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if n := f.count("ins_counter"); n != 1 {
		t.Fatalf("reservation inserts after one lease = %d, want 1", n)
	}
	var id string
	if err := f.db.QueryRow(`SELECT id FROM ins_counter`).Scan(&id); err != nil || id != first.ReservationID {
		t.Fatalf("inserted reservation id = (%q, %v), want lease.ReservationID %q", id, err, first.ReservationID)
	}
	if _, err := f.fleet.Lease(t.Context(), request(), dispatchFor("job-1"), 1); err != nil {
		t.Fatalf("second Lease: %v", err)
	}
	if n := f.count("ins_counter"); n != 2 {
		t.Fatalf("reservation inserts after two leases = %d, want 2", n)
	}
}

func TestFleetReleaseIdempotent(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	l, err := f.fleet.Lease(t.Context(), request(), scopedDispatch("job-1"), 1)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if err := f.fleet.Release(t.Context(), l); err != nil {
		t.Fatalf("Release: %v", err)
	}
	f.mustState(economics.ReservationReleased)
	if got := f.calls.all(); !slices.Equal(got, reverseTeardown) {
		t.Fatalf("calls = %v, want the reverse teardown %v", got, reverseTeardown)
	}
	if err := f.fleet.Release(t.Context(), l); err != nil {
		t.Fatalf("second Release = %v, want nil", err)
	}
	if got := f.calls.all(); len(got) != len(reverseTeardown) {
		t.Fatalf("calls after a second Release = %v, want no second teardown", got)
	}
}

func TestFleetLeaseConcurrentAcquireRelease(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := dispatchFor("job-1")
			d.RepoID, d.ScopeGlobs = "repo-a", []string{fmt.Sprintf("pkg%d/**", i)}
			l, err := f.fleet.Lease(context.Background(), request(), d, 1)
			if err == nil {
				err = f.fleet.Release(context.Background(), l)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Lease+Release: %v", err)
		}
	}
	if held, _ := f.store.ListByState(t.Context(), economics.ReservationHeld); len(held) != 0 {
		t.Fatalf("%d held rows remain, want none", len(held))
	}
	rs, err := f.store.ListByState(t.Context(), economics.ReservationReleased)
	if err != nil || len(rs) != 16 {
		t.Fatalf("released rows = (%d, %v), want 16", len(rs), err)
	}
	released := f.book.releasedIDs()
	if f.book.liveCount() != 0 || len(released) != 16 || len(slices.Compact(slices.Sorted(slices.Values(released)))) != 16 {
		t.Fatalf("live leases %d, released %v, want 16 distinct leases released and none live", f.book.liveCount(), released)
	}
	if n := len(f.fleet.index.entries); n != 0 {
		t.Fatalf("lease index holds %d entries, want 0", n)
	}
}

func TestFleetLeaseCompensatingRelease(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	l, err := f.fleet.Lease(t.Context(), request(), scopedDispatch("job-1"), 1)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	for i := range 2 {
		if err := f.fleet.Abort(t.Context(), l); err != nil {
			t.Fatalf("Abort #%d = %v, want nil (idempotent)", i+1, err)
		}
	}
	f.mustState(economics.ReservationRolledBack)
	if got := f.calls.all(); !slices.Equal(got, reverseTeardown) {
		t.Fatalf("calls = %v, want one reverse teardown %v", got, reverseTeardown)
	}
	if err := f.fleet.Release(t.Context(), l); err != nil {
		t.Fatalf("Release after Abort = %v, want nil", err)
	}

	g := newFx(t)
	g.seedJob("job-1", provider.SensitivityInternal)
	committed, err := g.fleet.Lease(t.Context(), request(), scopedDispatch("job-1"), 1)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if err := g.fleet.Commit(t.Context(), committed); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := g.fleet.Release(t.Context(), committed); err != nil {
		t.Fatalf("Release: %v", err)
	}
	g.mustState(economics.ReservationReleased)
	err = g.fleet.Commit(t.Context(), committed)
	if !cascade.HasKind(err, cascade.KindConflict) || !strings.Contains(err.Error(), "is released, not committed") {
		t.Fatalf("Commit of a released lease = %v, want a conflict naming the released state", err)
	}
}

func TestFleetLeaseExpireStaleRetiresUnheldRows(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	other, err := economics.NewReserver(f.store, f.clock, "epoch-2", f.seams())
	if err != nil {
		t.Fatalf("NewReserver: %v", err)
	}
	f.deps.Reserver = other
	f.rebuild()
	l, err := f.fleet.Lease(t.Context(), request(), scopedDispatch("job-1"), 1)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	f.deps.Reserver = f.reserver // the daemon that does not hold the row
	f.rebuild()
	f.clock.Advance(4 * f.deps.TTL)
	n, err := f.fleet.ExpireStale(t.Context())
	if err != nil || n != 1 {
		t.Fatalf("ExpireStale = (%d, %v), want 1", n, err)
	}
	f.mustState(economics.ReservationRolledBack)
	if again, err := f.fleet.ExpireStale(t.Context()); err != nil || again != 0 {
		t.Fatalf("second ExpireStale = (%d, %v), want 0", again, err)
	}
	if err := f.fleet.Release(t.Context(), l); err != nil {
		t.Fatalf("Release of an expired lease = %v, want nil", err)
	}
}

func TestFleetLeaseReadableByExecution(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	l, err := f.fleet.Lease(t.Context(), request(), dispatchFor("job-1"), 1)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	r, ok, err := f.fleet.ReservationByExecution(t.Context(), l.ExecutionID)
	if err != nil || !ok || r.ID != l.ReservationID || r.JobID != "job-1" {
		t.Fatalf("ReservationByExecution = (%+v, %v, %v), want the lease's reservation", r, ok, err)
	}
	if _, ok, err := f.fleet.ReservationByExecution(t.Context(), "exec-never-leased"); ok || err != nil {
		t.Fatalf("unknown execution = (%v, %v), want (false, nil)", ok, err)
	}
}
