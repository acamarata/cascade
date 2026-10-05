package economics

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestTeardownLIFOOrder is the direct atomicity/LIFO proof: a
// reservation that acquired BOTH leases and a worktree must unwind in the
// EXACT reverse of application order, proven through Release, which runs
// the identical reverse chain rollback uses.
func TestTeardownLIFOOrder(t *testing.T) {
	f := newReserverFixture(t, 100000)
	rv := f.reserver(t)
	held := baseReservation("r-lifo")
	held.LeaseIDs = []string{"lease-1"}
	held.WorktreeID = "worktree-1"
	if _, err := f.store.Insert(t.Context(), held); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, err := rv.Release(t.Context(), "r-lifo"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	want := []string{"remove_worktree", "release_leases"}
	if got := f.rec.snapshot(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("unwind order = %v, want %v (LIFO: worktree before leases)", got, want)
	}
}

// TestRollbackPolicyJoinsOriginalAndCompensationErrors: when a
// compensation itself errors during rollback, the returned error exposes
// BOTH the original trigger AND ErrReservationRollback, every
// compensation is still attempted, and the row still lands rolled_back.
func TestRollbackPolicyJoinsOriginalAndCompensationErrors(t *testing.T) {
	f := newReserverFixture(t, 100000)
	triggerErr := errors.New("worktree allocation failed")
	compensationErr := errors.New("lease release also failed")
	f.seams.AllocateWorktree = func(context.Context, string) (string, error) { return "", triggerErr }
	f.seams.ReleaseLeases = func(context.Context, []string) error {
		f.rec.record("release_leases")
		return compensationErr
	}
	rv := f.reserver(t)
	r, err := rv.Reserve(t.Context(), baseReserveRequest())
	if !hasIdentity(err, triggerErr) || !hasIdentity(err, compensationErr) {
		t.Errorf("err = %v, want both the trigger and the compensation error", err)
	}
	requireSentinel(t, err, ErrReservationRollback)
	stored, ok, getErr := f.store.Get(t.Context(), r.ID)
	if getErr != nil || !ok || stored.State != ReservationRolledBack {
		t.Errorf("persisted = %+v ok=%v err=%v, want rolled_back even though compensation failed", stored, ok, getErr)
	}
	if got := f.rec.snapshot(); fmt.Sprint(got) != "[acquire_leases release_leases]" {
		t.Errorf("calls = %v, want release attempted once", got)
	}
}

// TestReserveCommitReleaseIdempotent: Commit moves held -> committed
// with no acquisition; Release performs the reverse teardown and records
// released; a second Commit/Release returns the stored row unchanged.
func TestReserveCommitReleaseIdempotent(t *testing.T) {
	f := newReserverFixture(t, 100000)
	rv := f.reserver(t)
	r, err := rv.Reserve(t.Context(), baseReserveRequest())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	before := len(f.rec.snapshot())
	committed, err := rv.Commit(t.Context(), r.ID)
	if err != nil || committed.State != ReservationCommitted || len(f.rec.snapshot()) != before {
		t.Fatalf("Commit = %+v, %v; calls %v, want committed with no acquisition", committed, err, f.rec.snapshot())
	}
	again, err := rv.Commit(t.Context(), r.ID)
	if err != nil || fmt.Sprint(again) != fmt.Sprint(committed) {
		t.Fatalf("second Commit = %+v, %v, want the stored row unchanged", again, err)
	}
	released, err := rv.Release(t.Context(), r.ID)
	if err != nil || released.State != ReservationReleased || len(released.LeaseIDs) != 0 || released.WorktreeID != "" {
		t.Fatalf("Release = %+v, %v, want released with every step compensated", released, err)
	}
	want := "[acquire_leases allocate_worktree remove_worktree release_leases]"
	if got := f.rec.snapshot(); fmt.Sprint(got) != want {
		t.Fatalf("calls = %v, want %s", got, want)
	}
	stored, _, _ := f.store.Get(t.Context(), r.ID)
	for _, id := range []string{"commit", "release"} {
		var got Reservation
		if id == "commit" {
			got, err = rv.Commit(t.Context(), r.ID)
		} else {
			got, err = rv.Release(t.Context(), r.ID)
		}
		if err != nil || fmt.Sprint(got) != fmt.Sprint(stored) {
			t.Errorf("%s on a released row = %+v, %v, want the stored row unchanged", id, got, err)
		}
	}
	if got := f.rec.snapshot(); fmt.Sprint(got) != want {
		t.Errorf("repeat Commit/Release ran seams: %v", got)
	}
}

// TestReserveConcurrentRaceExactlyOneWins runs two Reserve calls
// concurrently against a domain whose capacity fits exactly one. Under
// -race exactly one lands held and the other rolled_back with no residue.
func TestReserveConcurrentRaceExactlyOneWins(t *testing.T) {
	f := newReserverFixture(t, 50)
	rv := f.reserver(t)
	var wg sync.WaitGroup
	results := make([]Reservation, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := baseReserveRequest()
			req.Estimate = Estimate{TokensIn: 50}
			req.JobID = fmt.Sprintf("job-%d", idx)
			req.ProjectID = fmt.Sprintf("project-%d", idx)
			results[idx], errs[idx] = rv.Reserve(context.Background(), req)
		}(i)
	}
	wg.Wait()
	var wins, losses int
	for i := 0; i < 2; i++ {
		switch {
		case errs[i] == nil && results[i].State == ReservationHeld:
			wins++
		case hasIdentity(errs[i], ErrQuotaUnavailable) && results[i].State == ReservationRolledBack:
			losses++
			if len(results[i].LeaseIDs) != 0 || results[i].WorktreeID != "" {
				t.Errorf("loser %d carries residue: %+v", i, results[i])
			}
		default:
			t.Errorf("result %d neither a clean win nor a clean loss: r=%+v err=%v", i, results[i], errs[i])
		}
	}
	if wins != 1 || losses != 1 {
		t.Fatalf("wins=%d losses=%d, want exactly 1 and 1", wins, losses)
	}
}

func TestReserveRejectsMissingIdentityFields(t *testing.T) {
	f := newReserverFixture(t, 100000)
	rv := f.reserver(t)
	cases := map[string]func(*ReserveRequest){
		"DomainID":    func(r *ReserveRequest) { r.DomainID = "" },
		"ExecutionID": func(r *ReserveRequest) { r.ExecutionID = "" },
		"RepoID":      func(r *ReserveRequest) { r.RepoID = "" },
		"JobID":       func(r *ReserveRequest) { r.JobID = "" },
	}
	for name, mutate := range cases {
		req := baseReserveRequest()
		mutate(&req)
		if _, err := rv.Reserve(t.Context(), req); !isKindInvalidInput(err) {
			t.Errorf("Reserve(empty %s) = %v, want KindInvalidInput", name, err)
		}
	}
	quotaOnly := baseReserveRequest()
	quotaOnly.ScopeGlobs, quotaOnly.JobID, quotaOnly.RepoID = nil, "", ""
	if _, err := rv.Reserve(t.Context(), quotaOnly); err != nil {
		t.Errorf("Reserve(no scope globs, no job) = %v, want a quota-only lease", err)
	}
	if got := f.rec.snapshot(); len(got) != 0 {
		t.Errorf("refused or quota-only requests ran seams: %v", got)
	}
}

func TestCommitNotFound(t *testing.T) {
	f := newReserverFixture(t, 100000)
	rv := f.reserver(t)
	if _, err := rv.Commit(t.Context(), "never-created"); err == nil {
		t.Error("Commit(missing id) = nil error, want not-found error")
	}
	if _, err := rv.Release(t.Context(), "never-created"); err == nil {
		t.Error("Release(missing id) = nil error, want not-found error")
	}
}

// TestFailedCompensationStaysRecordedOnTerminalRow pins the base rollback
// policy: a RemoveWorktree failure during Release is returned (joined
// under ErrReservationRollback), the row still lands released, the
// leaked worktree id stays on the row for an operator to find, and
// nothing retries it: a later Sweep and ExpireStale leave the row alone.
func TestFailedCompensationStaysRecordedOnTerminalRow(t *testing.T) {
	f := newReserverFixture(t, 100000)
	cause := errors.New("worktree busy")
	removes := 0
	f.seams.RemoveWorktree = func(context.Context, string) error { removes++; return cause }
	rv := f.reserver(t)
	r, err := rv.Reserve(t.Context(), baseReserveRequest())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	_, err = rv.Release(t.Context(), r.ID)
	requireSentinel(t, err, ErrReservationRollback, "worktree busy")
	stored, _, _ := f.store.Get(t.Context(), r.ID)
	if stored.State != ReservationReleased || stored.WorktreeID != "worktree-1" || len(stored.LeaseIDs) != 0 {
		t.Fatalf("stored = %+v, want released, worktree-1 still recorded, leases released", stored)
	}
	if rep, err := rv.Sweep(t.Context(), func(context.Context) (bool, error) { return false, nil }); err != nil || rep != (SweepReport{}) {
		t.Fatalf("Sweep = %+v, %v, want nothing to do", rep, err)
	}
	f.clock.Advance(4 * hbInterval)
	if n, err := rv.ExpireStale(t.Context(), hbInterval); err != nil || n != 0 || removes != 1 {
		t.Fatalf("ExpireStale = %d, %v, removes %d, want 0, nil and no retry", n, err, removes)
	}
}

// TestNestedExpireStaleRetiresOnce: an ExpireStale that runs while another
// is tearing a row down (here, from inside its RemoveWorktree seam) skips
// that row: one teardown, one retirement, and neither call errors.
func TestNestedExpireStaleRetiresOnce(t *testing.T) {
	f := newReserverFixture(t, 100000)
	insertUnheld(t, f, "r-a", ReservationCommitted, 0)
	var rv *Reserver
	removes, inner := 0, -1
	var innerErr error
	f.seams.RemoveWorktree = func(ctx context.Context, _ string) error {
		removes++
		if removes == 1 {
			inner, innerErr = rv.ExpireStale(ctx, hbInterval)
		}
		return nil
	}
	rv = f.reserver(t)
	f.clock.Advance(4 * hbInterval)
	n, err := rv.ExpireStale(t.Context(), hbInterval)
	if err != nil || n != 1 || inner != 0 || innerErr != nil || removes != 1 {
		t.Fatalf("outer = %d, %v; inner = %d, %v; removes %d, want 1, nil; 0, nil; 1", n, err, inner, innerErr, removes)
	}
	if s, _, _ := f.store.Get(t.Context(), "r-a"); s.State != ReservationReleased {
		t.Fatalf("r-a = %s, want released", s.State)
	}
}

// TestExpireStaleSkipsRowThatChangedStateAfterListing: r-b is listed
// stale as held, then moves to committed while r-a's teardown runs. The
// fence is keyed to the listed state, so r-b is skipped: not retired, not
// counted, owner_epoch unchanged, and no teardown seam runs for it.
func TestExpireStaleSkipsRowThatChangedStateAfterListing(t *testing.T) {
	f := newReserverFixture(t, 100000)
	now := f.clock.Now().Unix()
	insertUnheld(t, f, "r-a", ReservationHeld, now)
	f.clock.Advance(time.Second)
	insertUnheld(t, f, "r-b", ReservationHeld, now)
	var removed, released []string
	var moved bool
	var moveErr error
	f.seams.RemoveWorktree = func(ctx context.Context, wt string) error {
		removed = append(removed, wt)
		if wt == "r-a-wt" {
			moved, moveErr = f.store.casState(ctx, "r-b", ReservationHeld, ReservationCommitted)
		}
		return nil
	}
	f.seams.ReleaseLeases = func(_ context.Context, ids []string) error {
		released = append(released, ids...)
		return nil
	}
	rv := f.reserverWithEpoch(t, "epoch-A")
	f.clock.Advance(4 * hbInterval)
	n, err := rv.ExpireStale(t.Context(), hbInterval)
	if err != nil || n != 1 || moveErr != nil || !moved {
		t.Fatalf("ExpireStale = %d, %v (state change landed=%v, %v), want exactly r-a retired", n, err, moved, moveErr)
	}
	b, _, _ := f.store.Get(t.Context(), "r-b")
	if b.State != ReservationCommitted || b.OwnerEpoch != "epoch-dead" || b.WorktreeID != "r-b-wt" || len(b.LeaseIDs) != 1 {
		t.Errorf("r-b = %+v, want committed, still owned by epoch-dead, handles intact", b)
	}
	if fmt.Sprint(removed) != "[r-a-wt]" || fmt.Sprint(released) != "[r-a-lease]" {
		t.Errorf("teardown removed=%v released=%v, want only r-a's handles", removed, released)
	}
}
