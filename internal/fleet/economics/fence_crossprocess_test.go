package economics

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestFencedRowSkippedBySecondSweeper uses two database handles on one
// SQLite file to exercise the cross-process fence window.
func TestFencedRowSkippedBySecondSweeper(t *testing.T) {
	for _, adopt := range []bool{false, true} {
		name := "foreign_epoch"
		if adopt {
			name = "self_epoch"
		}
		t.Run(name, func(t *testing.T) { testSecondSweeperFence(t, adopt) })
	}
}

func testSecondSweeperFence(t *testing.T, adopt bool) {
	f, storeB := openTwoReservationStores(t)
	insertUnheld(t, f, "r-a", ReservationCommitted, f.clock.Now().Unix())
	var b *Reserver
	var nested bool
	var nestedN int
	var nestedErr error
	f.seams.RemoveWorktree = func(ctx context.Context, wt string) error {
		f.rec.record("remove_worktree")
		if wt == "r-a-wt" && !nested {
			nested = true
			nestedN, nestedErr = b.ExpireStale(ctx, hbInterval)
		}
		return nil
	}
	a := f.reserverWithEpoch(t, "epoch-A")
	var err error
	b, err = NewReserver(storeB, f.clock, "epoch-B", f.seams)
	if err != nil {
		t.Fatalf("NewReserver(B): %v", err)
	}
	if adopt {
		if rep, err := a.Sweep(t.Context(), notLive); err != nil || rep.Adopted != 1 {
			t.Fatalf("A.Sweep = %+v, %v, want one adoption", rep, err)
		}
	}
	f.clock.Advance(4 * hbInterval)
	retired, err := a.ExpireStale(t.Context(), hbInterval)
	if !nested || nestedN != 0 || nestedErr != nil {
		t.Errorf("B.ExpireStale inside teardown = %d, %v, want 0, nil", nestedN, nestedErr)
	}
	if err != nil || retired != 1 {
		t.Fatalf("A.ExpireStale = %d, %v, want 1, nil", retired, err)
	}
	if calls := f.rec.snapshot(); countCalls(calls, "remove_worktree") != 1 || countCalls(calls, "release_leases") != 1 {
		t.Fatalf("teardown calls = %v, want exactly one remove and release", calls)
	}
	row, ok, err := f.store.Get(t.Context(), "r-a")
	if err != nil || !ok || row.State != ReservationReleased || row.OwnerEpoch != "epoch-A" {
		t.Fatalf("stored row = %+v, ok=%v err=%v, want released by epoch-A", row, ok, err)
	}
}

// TestFencedOwnerCannotRenew checks both the direct refusal and Heartbeat's
// untrack-without-renew behavior while another handle owns the fence.
func TestFencedOwnerCannotRenew(t *testing.T) {
	f, storeB := openTwoReservationStores(t)
	insertUnheld(t, f, "r-3", ReservationCommitted, f.clock.Now().Unix())
	var b *Reserver
	var renewedIDs []string
	f.seams.RenewLeases = func(_ context.Context, ids []string) error {
		renewedIDs = append(renewedIDs, ids...)
		return nil
	}
	var err error
	b, err = NewReserver(storeB, f.clock, "epoch-B", f.seams)
	if err != nil {
		t.Fatalf("NewReserver(B): %v", err)
	}
	if rep, err := b.Sweep(t.Context(), notLive); err != nil || rep.Adopted != 1 {
		t.Fatalf("B.Sweep(r-3) = %+v, %v, want one adoption", rep, err)
	}
	if err := b.Adopt(t.Context(), "r-3"); err != nil {
		t.Fatalf("B.Adopt(r-3): %v", err)
	}
	f.clock.Advance(4 * hbInterval)
	insertUnheld(t, f, "r-4", ReservationCommitted, f.clock.Now().Unix())
	if rep, err := b.Sweep(t.Context(), notLive); err != nil || rep.Adopted != 1 {
		t.Fatalf("B.Sweep(r-4) = %+v, %v, want one adoption", rep, err)
	}
	if err := b.Adopt(t.Context(), "r-4"); err != nil {
		t.Fatalf("B.Adopt(r-4): %v", err)
	}
	a := f.reserverWithEpoch(t, "epoch-A")
	// Probe the active fence before retirement claims the terminal state.
	probed := false
	f.store.beforeWrite = func(ctx context.Context, id string) {
		if id == "r-3" && !probed {
			probed = true
			probeFencedOwner(ctx, t, f, storeB, b, &renewedIDs)
		}
	}
	if n, err := a.ExpireStale(t.Context(), hbInterval); err != nil || n != 1 {
		t.Fatalf("A.ExpireStale = %d, %v, want one retirement", n, err)
	}
	if !probed {
		t.Fatal("retirement claim did not reach the fenced-owner probe")
	}
	verifyReleasedFencedRow(t, f, b)
}

func verifyReleasedFencedRow(t *testing.T, f *reserverFixture, b *Reserver) {
	t.Helper()
	row, ok, err := f.store.Get(t.Context(), "r-3")
	if err != nil || !ok || row.State != ReservationReleased || row.OwnerEpoch != "epoch-A" {
		t.Fatalf("r-3 = %+v, ok=%v err=%v, want released by epoch-A", row, ok, err)
	}
	if renewed, lost, err := b.Heartbeat(t.Context()); err != nil || renewed != 1 || lost != 0 {
		t.Fatalf("later B.Heartbeat = %d, %d, %v, want only r-4", renewed, lost, err)
	}
	requireSentinel(t, b.Adopt(t.Context(), "r-3"), ErrReservationTransition, "released")
}

func probeFencedOwner(ctx context.Context, t *testing.T, f *reserverFixture, storeB *ReservationStore, b *Reserver, renewedIDs *[]string) {
	t.Helper()
	before, ok, err := f.store.Get(ctx, "r-3")
	if err != nil || !ok || before.OwnerEpoch != "epoch-A" {
		t.Fatalf("fenced row before owner renewal = %+v, ok=%v err=%v", before, ok, err)
	}
	f.clock.Advance(time.Second)
	_, _, err = storeB.touchHeartbeat(ctx, "r-3", "epoch-B", f.clock.Now().Unix())
	requireSentinel(t, err, ErrReservationFenced, "r-3", "epoch-A")
	after, _, getErr := f.store.Get(ctx, "r-3")
	if getErr != nil || fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("fenced row changed: before=%+v after=%+v err=%v", before, after, getErr)
	}
	if renewed, lost, err := b.Heartbeat(ctx); err != nil || renewed != 1 || lost != 0 {
		t.Fatalf("B.Heartbeat during fence = %d, %d, %v, want only r-4", renewed, lost, err)
	}
	if fmt.Sprint(*renewedIDs) != "[r-4-lease]" || b.isTracked("r-3") || !b.isTracked("r-4") {
		t.Fatalf("renewed=%v tracked r-3=%v r-4=%v", *renewedIDs, b.isTracked("r-3"), b.isTracked("r-4"))
	}
	if err := b.Adopt(ctx, "r-3"); !cascade.HasKind(err, cascade.KindConflict) || !strings.Contains(err.Error(), "epoch-A") {
		t.Fatalf("B.Adopt(r-3) during fence = %v, want conflict naming epoch-A", err)
	}
}

func openTwoReservationStores(t *testing.T) (*reserverFixture, *ReservationStore) {
	t.Helper()
	f := newReserverFixture(t, 100000)
	path := filepath.Join(t.TempDir(), "reservation.db")
	clock := f.clock
	storeA, err := NewReservationStore(openReservationTestDBAt(t, path), clock)
	if err != nil {
		t.Fatalf("NewReservationStore(A): %v", err)
	}
	storeB, err := NewReservationStore(openReservationTestDBAt(t, path), clock)
	if err != nil {
		t.Fatalf("NewReservationStore(B): %v", err)
	}
	f.store = storeA
	return f, storeB
}

func countCalls(calls []string, want string) int {
	n := 0
	for _, call := range calls {
		if call == want {
			n++
		}
	}
	return n
}
