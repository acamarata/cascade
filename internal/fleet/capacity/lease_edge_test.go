// Purpose: the adversarial and edge paths of Fleet.Lease and its companions: a
// Selector that rewrites its input, a reservation a rate-limit cascade parked,
// the Abort branches, the data class a nil request takes from its job, and the
// functional teardown of leased resources.
//
// SPORT: fleet.capacity.lease.

package capacity

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// localOnlyFleet is a fixture whose job is local-only, so only lane-t0 is eligible.
func localOnlyFleet(t *testing.T, pick func(context.Context, SelectInput) (Selection, error)) *fx {
	t.Helper()
	f := newFx(t)
	f.seedJob("job-local", provider.SensitivityLocalOnly)
	f.deps.Selector = selectorFunc(pick)
	f.rebuild()
	return f
}

func TestFleetLeaseSelectorCannotRewriteItsInput(t *testing.T) {
	f := localOnlyFleet(t, func(_ context.Context, in SelectInput) (Selection, error) {
		in.Eligible[0] = "lane-t2" // overwrite the eligible set, then answer with the lane the sensitivity filter removed
		return Selection{Lane: "lane-t2", Tier: TierSelection{Tier: TierTwo}}, nil
	})
	_, err := f.fleet.Lease(t.Context(), request(), dispatchFor("job-local"), 1)
	requireSentinel(t, err, ErrNoCapableTier, `lane "lane-t2" outside the eligible set`)
	f.zeroRows()
}

func TestFleetLeaseRefusesASelectionWithTheWrongTier(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	f.deps.Selector = selectorFunc(func(context.Context, SelectInput) (Selection, error) {
		return Selection{Lane: "lane-t2", Tier: TierSelection{Tier: TierZero}}, nil
	})
	f.rebuild()
	_, err := f.fleet.Lease(t.Context(), request(), dispatchFor("job-1"), 1)
	requireSentinel(t, err, ErrNoCapableTier, `tier "tier-0" for lane "lane-t2", which is tier "tier-2"`)
	f.zeroRows()
}

func TestFleetLeaseSelectorCannotReachTheSnapshotOwnersMaps(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	inner := f.deps.Selector
	f.deps.Selector = selectorFunc(func(ctx context.Context, in SelectInput) (Selection, error) {
		delete(in.Snapshot.Lanes, "lane-t1")
		slot := in.Snapshot.Lanes["lane-t0"]
		slot.State = StateExhausted
		in.Snapshot.Lanes["lane-t0"] = slot
		return inner.Select(ctx, in)
	})
	f.rebuild()
	if _, err := f.fleet.Lease(t.Context(), request(), dispatchFor("job-1"), 1); err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if _, ok := f.snap.Lanes["lane-t1"]; !ok || f.snap.Lanes["lane-t0"].State != StateAvailable {
		t.Fatalf("the snapshot owner's lanes were changed by the Selector: %+v", f.snap.Lanes)
	}
}

func TestFleetLeaseNilDataClassTakesTheJobsClass(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-public", provider.SensitivityPublic)
	req := request()
	req.Sensitivity, req.DataClass = provider.SensitivityPublic, nil
	l, err := f.fleet.Lease(t.Context(), req, dispatchFor("job-public"), 1)
	if err != nil || l.Sensitivity != provider.SensitivityPublic || l.LaneID != "lane-t2" {
		t.Fatalf("Lease = (%+v, %v), want the job's public class to open the external lane", l, err)
	}
	f.seedJob("job-restricted", provider.SensitivityRestricted)
	req.Sensitivity = provider.SensitivityPublic
	l, err = f.fleet.Lease(t.Context(), req, dispatchFor("job-restricted"), 1)
	if err != nil || l.Sensitivity != provider.SensitivityRestricted || l.LaneID != "lane-t0" {
		t.Fatalf("Lease = (%+v, %v), want the job's restricted class to join over the request's public tier and keep the work local", l, err)
	}
}

func TestFleetLeaseParkedReservationIsReleased(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	f.onAcquire = func(ctx context.Context) { _, _ = f.reserver.CascadeRateLimited(ctx, "scope-1") }
	_, err := f.fleet.Lease(t.Context(), request(), scopedDispatch("job-1"), 1)
	requireSentinel(t, err, ErrReservationFailed, "a rate-limit cascade parked the reservation", "is parked, not held")
	f.mustState(economics.ReservationReleased)
	if n := f.book.liveCount(); n != 0 || f.count("jobs_execution") != 0 || f.count("test_decision") != 0 {
		t.Fatalf("live leases %d, executions %d, decisions %d, want a clean refusal", n, f.count("jobs_execution"), f.count("test_decision"))
	}

	g := newFx(t)
	g.seedJob("job-1", provider.SensitivityInternal)
	g.onAcquire = func(ctx context.Context) { _, _ = g.reserver.CascadeRateLimited(ctx, "scope-1") }
	g.releaseErr = errors.New("lease service unreachable")
	_, err = g.fleet.Lease(t.Context(), request(), scopedDispatch("job-1"), 1)
	requireSentinel(t, err, ErrReservationFailed, "could not be released", "lease service unreachable")
}

// abortLease leases one internal job on the fixture.
func abortLease(t *testing.T, f *fx) Lease {
	t.Helper()
	f.seedJob("job-1", provider.SensitivityInternal)
	l, err := f.fleet.Lease(t.Context(), request(), scopedDispatch("job-1"), 1)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	return l
}

func TestFleetAbortBranches(t *testing.T) {
	lease := func(f *fx) Lease { return abortLease(t, f) }
	t.Run("committed retires through release", func(t *testing.T) {
		f := newFx(t)
		l := lease(f)
		if err := f.fleet.Commit(t.Context(), l); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		if err := f.fleet.Abort(t.Context(), l); err != nil {
			t.Fatalf("Abort of a committed lease = %v, want nil", err)
		}
		f.mustState(economics.ReservationReleased)
	})
	t.Run("parked retires through release", func(t *testing.T) {
		f := newFx(t)
		l := lease(f)
		if _, err := f.reserver.Park(t.Context(), l.ReservationID); err != nil {
			t.Fatalf("Park: %v", err)
		}
		if err := f.fleet.Abort(t.Context(), l); err != nil {
			t.Fatalf("Abort of a parked lease = %v, want nil", err)
		}
		f.mustState(economics.ReservationReleased)
	})
	t.Run("a missing reservation is not found", func(t *testing.T) {
		f := newFx(t)
		l := lease(f)
		l.ExecutionID = "exec-never-leased"
		err := f.fleet.Abort(t.Context(), l)
		if !cascade.HasKind(err, cascade.KindNotFound) {
			t.Fatalf("Abort = %v, want KindNotFound", err)
		}
	})
	t.Run("a lease naming another reservation is refused", func(t *testing.T) {
		f := newFx(t)
		l := lease(f)
		l.ReservationID = "reservation-forged"
		requireSentinel(t, f.fleet.Abort(t.Context(), l), ErrLeaseUnknown, "reservation-forged")
		if held, _ := f.store.ListByState(t.Context(), economics.ReservationHeld); len(held) != 1 {
			t.Fatalf("%d held rows, want the real reservation untouched", len(held))
		}
	})
}

func TestFleetAbortFailureBranches(t *testing.T) {
	lease := func(f *fx) Lease { return abortLease(t, f) }
	t.Run("a cancelled caller still rolls back", func(t *testing.T) {
		f := newFx(t)
		l := lease(f)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := f.fleet.Abort(ctx, l); err != nil {
			t.Fatalf("Abort with a cancelled context = %v, want nil", err)
		}
		f.mustState(economics.ReservationRolledBack)
	})
	t.Run("a failed teardown is reported", func(t *testing.T) {
		f := newFx(t)
		l := lease(f)
		f.removeErr = errors.New("worktree is busy")
		err := f.fleet.Abort(t.Context(), l)
		requireSentinel(t, err, ErrReservationFailed, "abort lease", "worktree is busy")
		if _, kept := f.fleet.index.get(l.ID); !kept {
			t.Fatal("the lease index dropped a lease whose teardown failed")
		}
	})
}

func TestFleetLeaseTeardownReleasesExactlyWhatWasAcquired(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	d := scopedDispatch("job-1")
	d.ScopeGlobs = []string{"pkg/**", "cmd/**"}
	l, err := f.fleet.Lease(t.Context(), request(), d, 1)
	if err != nil || len(l.RepoLeaseIDs) != 2 || f.book.liveCount() != 2 {
		t.Fatalf("Lease = (%+v, %v), live %d, want two held leases", l, err, f.book.liveCount())
	}
	if err := f.fleet.Release(t.Context(), l); err != nil {
		t.Fatalf("Release: %v", err)
	}
	// economics hands the lease set to the release seam in acquisition order, in
	// one call; the reverse is between subsystems (worktree first), checked below.
	released, want := f.book.releasedIDs(), l.RepoLeaseIDs
	if !slices.Equal(released, want) || f.book.liveCount() != 0 || len(f.book.removed) != 1 {
		t.Fatalf("released %v (live %d, worktrees removed %v), want exactly %v", released, f.book.liveCount(), f.book.removed, want)
	}
	if got := f.calls.all(); !slices.Equal(got, reverseTeardown) {
		t.Fatalf("calls = %v, want the worktree removed before the leases are released: %v", got, reverseTeardown)
	}
}

func TestFleetExpireStalePrunesTheLeaseIndex(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	kept, err := f.fleet.Lease(t.Context(), request(), repoDispatch("repo-a"), 1)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	gone, err := f.fleet.Lease(t.Context(), request(), repoDispatch("repo-b"), 1)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if _, err := f.reserver.Release(t.Context(), gone.ReservationID); err != nil {
		t.Fatalf("Reserver.Release: %v", err)
	}
	if _, err := f.fleet.ExpireStale(t.Context()); err != nil {
		t.Fatalf("ExpireStale: %v", err)
	}
	if _, held := f.fleet.index.get(gone.ID); held {
		t.Fatal("ExpireStale left the index entry of a retired lease")
	}
	if _, held := f.fleet.index.get(kept.ID); !held {
		t.Fatal("ExpireStale pruned the index entry of a live lease")
	}
}

func TestFleetLeaseRefusesAJobRaisedAfterPlanning(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityPublic)
	req := request()
	req.Sensitivity = provider.SensitivityPublic
	f.onAcquire = func(context.Context) { f.seedJob("job-1", provider.SensitivityLocalOnly) }
	_, err := f.fleet.Lease(t.Context(), req, scopedDispatch("job-1"), 1)
	requireSentinel(t, err, ErrReservationFailed, "bind the placement of lane", "now local-only")
	requireSentinel(t, err, ErrDataClassDenied)
	if n := f.count("jobs_execution"); n != 0 {
		t.Errorf("jobs_execution has %d rows, want 0", n)
	}
	f.mustState(economics.ReservationRolledBack)
	if n := f.book.liveCount(); n != 0 {
		t.Errorf("%d subordinate leases still held, want 0", n)
	}
}

func TestFleetReleaseOwnership(t *testing.T) {
	t.Run("a lease naming another reservation is refused", func(t *testing.T) {
		f := newFx(t)
		a := abortLease(t, f)
		d := scopedDispatch("job-1")
		d.RepoID, d.ScopeGlobs = "repo-b", []string{"other/**"}
		b, err := f.fleet.Lease(t.Context(), request(), d, 1)
		if err != nil {
			t.Fatalf("Lease b: %v", err)
		}
		mixed := a
		mixed.ReservationID = b.ReservationID
		requireSentinel(t, f.fleet.Release(t.Context(), mixed), ErrLeaseUnknown, b.ReservationID)
		if held, _ := f.store.ListByState(t.Context(), economics.ReservationHeld); len(held) != 2 {
			t.Fatalf("%d held rows, want both reservations untouched", len(held))
		}
	})
	t.Run("a cancelled caller still releases", func(t *testing.T) {
		f := newFx(t)
		l := abortLease(t, f)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := f.fleet.Release(ctx, l); err != nil {
			t.Fatalf("Release with a cancelled context = %v, want nil", err)
		}
		f.mustState(economics.ReservationReleased)
	})
	t.Run("an unknown execution is not found", func(t *testing.T) {
		f := newFx(t)
		l := abortLease(t, f)
		l.ExecutionID = "exec-never-leased"
		err := f.fleet.Release(t.Context(), l)
		if !cascade.HasKind(err, cascade.KindNotFound) || hasSentinel(err, ErrLeaseUnknown) || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("Release = %v, want the not-found refusal of the missing execution", err)
		}
	})
}
