package economics

// Purpose: prove owner release and a second process's expiry fence share
// one terminal claim before any compensation.
// Inputs: two real SQLite handles, an injected clock and channel barriers.
// Outputs: persisted terminal state and exactly one teardown per row.
// Constraints: no sleeps; both claim orderings repeat 200 times under race.
// SPORT: fleet/economics/reservation/ADD.

import (
	"context"
	"fmt"
	"testing"
)

func TestReleaseVersusSweeperTearsDownOnce(t *testing.T) {
	for _, ownerFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("owner_first_%v", ownerFirst), func(t *testing.T) {
			f, storeB := openTwoReservationStores(t)
			sweeper, err := NewReserver(storeB, f.clock, "epoch-sweeper", f.seams)
			if err != nil {
				t.Fatal(err)
			}
			for i := range 200 {
				r := baseReservation(fmt.Sprintf("release-race-%d", i))
				r.State = ReservationCommitted
				r.HeartbeatAt = f.clock.Now().Add(-4 * hbInterval).Unix()
				r.WorktreeID, r.LeaseIDs = r.ID+"-wt", []string{r.ID + "-lease"}
				r.Steps = []Step{
					{Step: StepLeases, State: StepAcquired, Handle: r.LeaseIDs[0]},
					{Step: StepWorktree, State: StepAcquired, Handle: r.WorktreeID},
				}
				if _, err := f.store.Insert(t.Context(), r); err != nil {
					t.Fatal(err)
				}
				before := len(f.rec.snapshot())
				if ownerFirst {
					releaseBeforeSweep(t, f, sweeper, r)
				} else {
					sweepBeforeRelease(t, f, sweeper, r)
				}
				assertRetiredOnce(t, f, r.ID, before)
			}
		})
	}
}

// releaseBeforeSweep pauses Release at its first side effect while the
// second handle lists and attempts expiry of the same stale row.
func releaseBeforeSweep(t *testing.T, f *reserverFixture, sweeper *Reserver, r Reservation) {
	t.Helper()
	entered, swept := make(chan struct{}), make(chan struct{})
	f.seams.RemoveWorktree = func(context.Context, string) error {
		f.rec.record("remove_worktree")
		close(entered)
		<-swept
		return nil
	}
	owner := f.reserver(t)
	done := make(chan error, 1)
	go func() { _, err := owner.Release(t.Context(), r.ID); done <- err }()
	<-entered
	stored, ok, getErr := f.store.Get(t.Context(), r.ID)
	n, sweepErr := sweeper.ExpireStale(t.Context(), hbInterval)
	close(swept)
	releaseErr := <-done
	if getErr != nil || !ok || stored.State != ReservationReleased || stored.OwnerEpoch != r.OwnerEpoch {
		t.Errorf("before teardown: state=%s owner=%s ok=%v err=%v, want released by owner", stored.State, stored.OwnerEpoch, ok, getErr)
	}
	if n != 0 || sweepErr != nil || releaseErr != nil {
		t.Errorf("sweeper=%d/%v release=%v, want 0/nil and nil", n, sweepErr, releaseErr)
	}
}

// sweepBeforeRelease fences after the owner's read but before its claim,
// leaving the active row fenced until that stale Release has returned.
func sweepBeforeRelease(t *testing.T, f *reserverFixture, sweeper *Reserver, r Reservation) {
	t.Helper()
	entered, fenced := make(chan struct{}), make(chan struct{})
	f.store.beforeWrite = func(context.Context, string) {
		close(entered)
		<-fenced
	}
	owner := f.reserver(t)
	done := make(chan error, 1)
	go func() { _, err := owner.Release(t.Context(), r.ID); done <- err }()
	<-entered
	claimed, claimErr := sweeper.claimExpiry(t.Context(), r)
	close(fenced)
	releaseErr := <-done
	f.store.beforeWrite = nil
	if claimErr != nil || !claimed {
		t.Fatalf("sweeper claim = %v/%v, want true/nil", claimed, claimErr)
	}
	if releaseErr == nil {
		t.Error("stale owner Release succeeded after the sweeper fence")
	} else {
		requireSentinel(t, releaseErr, ErrReservationTransition, r.ID)
	}
	if err := sweeper.recoverAndRetire(t.Context(), r); err != nil {
		t.Fatalf("sweeper retirement: %v", err)
	}
	sweeper.dropExpiry(r.ID)
	stored, ok, err := f.store.Get(t.Context(), r.ID)
	if err != nil || !ok || stored.OwnerEpoch != "epoch-sweeper" {
		t.Fatalf("fence owner = %s, ok=%v err=%v", stored.OwnerEpoch, ok, err)
	}
}

func assertRetiredOnce(t *testing.T, f *reserverFixture, id string, before int) {
	t.Helper()
	calls := f.rec.snapshot()[before:]
	if countCalls(calls, "remove_worktree") != 1 || countCalls(calls, "release_leases") != 1 {
		t.Fatalf("teardown calls = %v, want exactly one remove and release", calls)
	}
	r, ok, err := f.store.Get(t.Context(), id)
	if err != nil || !ok || r.State != ReservationReleased || r.WorktreeID != "" || len(r.LeaseIDs) != 0 {
		t.Fatalf("retired row = %+v, ok=%v err=%v", r, ok, err)
	}
	for _, step := range []StepName{StepWorktree, StepLeases} {
		if got := findStep(r, step); got.State != StepCompensated {
			t.Fatalf("%s step = %+v, want compensated", step, got)
		}
	}
}
