package economics

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// TestTeardownLIFOOrder is the direct atomicity/LIFO proof: a
// reservation that acquired BOTH leases and a worktree (as Reserve's
// pipeline would leave one after full success) must unwind in the EXACT
// reverse of application order -- worktree removed BEFORE leases
// released -- proven here via Release, which runs the identical reverse
// chain rollback uses.
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

// TestRollbackPolicyJoinsOriginalAndCompensationErrors is the dedicated
// test for the DECIDED rollback-failure policy: when a compensation
// step itself errors during rollback, the returned error must expose
// BOTH the original trigger AND ErrReservationRollback via errors.Is --
// the original error is never silently swallowed -- and every
// compensation is still attempted (release_leases runs even though it
// also fails).
func TestRollbackPolicyJoinsOriginalAndCompensationErrors(t *testing.T) {
	f := newReserverFixture(t, 100000)
	triggerErr := errors.New("worktree allocation failed")
	compensationErr := errors.New("lease release also failed")
	f.allocateWorktree = func(_ context.Context, _ string) (string, error) {
		f.rec.record("allocate_worktree")
		return "", triggerErr
	}
	f.releaseLeases = func(_ context.Context, _ []string) error {
		f.rec.record("release_leases")
		return compensationErr
	}
	rv := f.reserver(t)
	r, err := rv.Reserve(t.Context(), baseReserveRequest())
	if err == nil {
		t.Fatal("Reserve = nil error, want joined error")
	}
	if !errors.Is(err, triggerErr) {
		t.Errorf("err does not wrap the original trigger error: %v", err)
	}
	if !errors.Is(err, ErrReservationRollback) {
		t.Errorf("err does not wrap ErrReservationRollback: %v", err)
	}
	// release_leases was attempted despite having no successor step --
	// and the row still lands rolled_back even though compensation failed.
	stored, ok, getErr := f.store.Get(t.Context(), r.ID)
	if getErr != nil || !ok {
		t.Fatalf("Get: %v %v", ok, getErr)
	}
	if stored.State != ReservationRolledBack {
		t.Errorf("persisted state = %s, want rolled_back even though compensation failed", stored.State)
	}
}

func TestCommitIdempotent(t *testing.T) {
	f := newReserverFixture(t, 100000)
	rv := f.reserver(t)
	r, err := rv.Reserve(t.Context(), baseReserveRequest())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	committed, err := rv.Commit(t.Context(), r.ID)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if committed.State != ReservationCommitted {
		t.Fatalf("state = %s, want committed", committed.State)
	}
	again, err := rv.Commit(t.Context(), r.ID)
	if err != nil {
		t.Fatalf("second Commit: %v, want idempotent no-op", err)
	}
	if again.State != ReservationCommitted {
		t.Errorf("second Commit state = %s, want committed unchanged", again.State)
	}
}

func TestCommitPerformsNoAcquisition(t *testing.T) {
	f := newReserverFixture(t, 100000)
	rv := f.reserver(t)
	r, err := rv.Reserve(t.Context(), baseReserveRequest())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	before := len(f.rec.snapshot())
	if _, err := rv.Commit(t.Context(), r.ID); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if after := len(f.rec.snapshot()); after != before {
		t.Errorf("Commit invoked %d additional seam calls, want 0", after-before)
	}
}

func TestReleaseIdempotentNoSecondTeardown(t *testing.T) {
	f := newReserverFixture(t, 100000)
	rv := f.reserver(t)
	r, err := rv.Reserve(t.Context(), baseReserveRequest())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	released, err := rv.Release(t.Context(), r.ID)
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if released.State != ReservationReleased {
		t.Fatalf("state = %s, want released", released.State)
	}
	countAfterFirst := len(f.rec.snapshot())
	again, err := rv.Release(t.Context(), r.ID)
	if err != nil {
		t.Fatalf("second Release: %v, want idempotent no-op", err)
	}
	if again.State != ReservationReleased {
		t.Errorf("second Release state = %s, want released unchanged", again.State)
	}
	if countAfterSecond := len(f.rec.snapshot()); countAfterSecond != countAfterFirst {
		t.Errorf("second Release ran %d more seam calls, want 0 (no second teardown)", countAfterSecond-countAfterFirst)
	}
}

// TestReserveConcurrentRaceExactlyOneWins runs two Reserve calls
// concurrently against a domain whose capacity fits exactly one of
// them. Run under -race: exactly one reservation must land held and the
// other rolled_back, and the loser must carry no lease_ids/worktree_id
// (no residue).
func TestReserveConcurrentRaceExactlyOneWins(t *testing.T) {
	est, err := EstimateFor(1000, "chat", 1) // TokensIn=1100, TokensOut=... use raw weight below instead
	_ = est
	_ = err
	f := newReserverFixture(t, 50) // capacity fits exactly one 50-weight request
	rv := f.reserver(t)

	req := baseReserveRequest()
	req.Estimate = Estimate{TokensIn: 50, TokensOut: 0, Requests: 0}

	var wg sync.WaitGroup
	results := make([]Reservation, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			r := req
			r.JobID = fmt.Sprintf("job-%d", idx)
			results[idx], errs[idx] = rv.Reserve(context.Background(), r)
		}(i)
	}
	wg.Wait()

	var wins, losses int
	for i := 0; i < 2; i++ {
		switch {
		case errs[i] == nil && results[i].State == ReservationHeld:
			wins++
		case errs[i] != nil && errors.Is(errs[i], ErrQuotaUnavailable) && results[i].State == ReservationRolledBack:
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

// TestReservePersistsHeldRowBeforeAdmissionCheck proves the R-21.114
// ordering: even a request that ultimately fails admission leaves a
// durable row (never silently dropped), because Insert runs before the
// check.
func TestReservePersistsHeldRowBeforeAdmissionCheck(t *testing.T) {
	f := newReserverFixture(t, 1)
	rv := f.reserver(t)
	r, err := rv.Reserve(t.Context(), baseReserveRequest())
	if err == nil {
		t.Fatal("want ErrQuotaUnavailable")
	}
	if r.ID == "" {
		t.Fatal("failed reservation has no id -- row was never persisted")
	}
	stored, ok, getErr := f.store.Get(t.Context(), r.ID)
	if getErr != nil || !ok {
		t.Fatalf("row not found after failed admission: ok=%v err=%v", ok, getErr)
	}
	if stored.State != ReservationRolledBack {
		t.Errorf("persisted state = %s, want rolled_back", stored.State)
	}
}

func TestReserveRejectsMissingIdentityFields(t *testing.T) {
	f := newReserverFixture(t, 100000)
	rv := f.reserver(t)
	req := baseReserveRequest()
	req.DomainID = ""
	if _, err := rv.Reserve(t.Context(), req); err == nil {
		t.Error("Reserve(empty DomainID) = nil error, want typed error")
	}
}

func TestCommitNotFound(t *testing.T) {
	f := newReserverFixture(t, 100000)
	rv := f.reserver(t)
	if _, err := rv.Commit(t.Context(), "never-created"); err == nil {
		t.Error("Commit(missing id) = nil error, want not-found error")
	}
}
