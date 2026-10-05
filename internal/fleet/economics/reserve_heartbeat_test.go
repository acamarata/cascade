package economics

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

const hbInterval = 30 * time.Second

// insertUnheld writes a row this Reserver does not hold in memory, with
// a heartbeat at the given unix second.
func insertUnheld(t *testing.T, f *reserverFixture, id string, state ReservationState, heartbeat int64) {
	t.Helper()
	r := baseReservation(id)
	r.State, r.HeartbeatAt, r.OwnerEpoch = state, heartbeat, "epoch-dead"
	r.LeaseIDs, r.WorktreeID = []string{id + "-lease"}, id+"-wt"
	if _, err := f.store.Insert(t.Context(), r); err != nil {
		t.Fatalf("Insert(%s): %v", id, err)
	}
}

// TestReserveHeartbeatAndExpireStale: Heartbeat renews heartbeat_at only
// for ids this Reserver holds; ExpireStale rolls back held/parked and
// releases committed rows whose heartbeat is older than 3 x interval,
// with reverse compensation, and leaves renewed rows alone.
func TestReserveHeartbeatAndExpireStale(t *testing.T) {
	f := newReserverFixture(t, 100000)
	rv := f.reserver(t)
	mine, err := rv.Reserve(t.Context(), baseReserveRequest())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	start := f.clock.Now().Unix()
	insertUnheld(t, f, "r-held", ReservationHeld, start)
	insertUnheld(t, f, "r-parked", ReservationParked, start)
	insertUnheld(t, f, "r-commit", ReservationCommitted, start)
	f.clock.Advance(3*hbInterval + time.Second)
	renewed, lost, err := rv.Heartbeat(t.Context())
	if err != nil || renewed != 1 || lost != 0 {
		t.Fatalf("Heartbeat = %d, %d, %v, want 1 renewed", renewed, lost, err)
	}
	got, _, _ := f.store.Get(t.Context(), mine.ID)
	unheld, _, _ := f.store.Get(t.Context(), "r-held")
	if got.HeartbeatAt != f.clock.Now().Unix() || unheld.HeartbeatAt != start {
		t.Fatalf("heartbeat_at mine=%d unheld=%d, want %d and %d", got.HeartbeatAt, unheld.HeartbeatAt, f.clock.Now().Unix(), start)
	}
	f.rec.logs = nil
	n, err := rv.ExpireStale(t.Context(), hbInterval)
	if err != nil || n != 3 {
		t.Fatalf("ExpireStale = %d, %v, want 3", n, err)
	}
	for id, want := range map[string]ReservationState{"r-held": ReservationRolledBack, "r-parked": ReservationRolledBack, "r-commit": ReservationReleased, mine.ID: ReservationHeld} {
		if r, _, _ := f.store.Get(t.Context(), id); r.State != want {
			t.Errorf("%s state = %s, want %s", id, r.State, want)
		}
	}
	calls := f.rec.snapshot()
	sort.Strings(calls)
	if fmt.Sprint(calls) != "[release_leases release_leases release_leases remove_worktree remove_worktree remove_worktree]" {
		t.Errorf("compensations = %v, want one worktree and one lease release per expired row", calls)
	}
	if again, err := rv.ExpireStale(t.Context(), hbInterval); err != nil || again != 0 {
		t.Errorf("second ExpireStale = %d, %v, want 0", again, err)
	}
}

// TestHeartbeatRenewsReservationLeases: Heartbeat calls RenewLeases with
// the lease ids of every in-memory reservation; a failed heartbeat_at
// write is returned, a lease refusal that is not a Conflict is not.
func TestHeartbeatRenewsReservationLeases(t *testing.T) {
	f := newReserverFixture(t, 100000)
	var mu sync.Mutex
	var renewedIDs []string
	f.seams.RenewLeases = func(_ context.Context, ids []string) error {
		mu.Lock()
		defer mu.Unlock()
		renewedIDs = append(renewedIDs, ids...)
		return nil
	}
	n := 0
	f.seams.AcquireLeases = func(context.Context, string, string, []string) ([]string, error) {
		n++
		return []string{fmt.Sprintf("lease-%d", n)}, nil
	}
	rv := f.reserver(t)
	for i := 0; i < 2; i++ {
		if _, err := rv.Reserve(t.Context(), baseReserveRequest()); err != nil {
			t.Fatalf("Reserve: %v", err)
		}
	}
	insertUnheld(t, f, "r-unheld", ReservationHeld, 0)
	if renewed, lost, err := rv.Heartbeat(t.Context()); err != nil || renewed != 2 || lost != 0 {
		t.Fatalf("Heartbeat = %d, %d, %v, want 2, 0, nil", renewed, lost, err)
	}
	sort.Strings(renewedIDs)
	if fmt.Sprint(renewedIDs) != "[lease-1 lease-2]" {
		t.Fatalf("RenewLeases saw %v, want exactly the two held reservations' leases", renewedIDs)
	}
	if _, err := f.store.DB().ExecContext(t.Context(), `CREATE TRIGGER refuse_hb BEFORE UPDATE OF heartbeat_at ON jobs_reservation BEGIN SELECT RAISE(ABORT, 'heartbeat write refused'); END`); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	if _, _, err := rv.Heartbeat(t.Context()); err == nil || !strings.Contains(err.Error(), "heartbeat write refused") {
		t.Fatalf("Heartbeat with a failing heartbeat_at write = %v, want that error", err)
	}
}

// TestHeartbeatTransientRenewRefusalIsNotAnError: a RenewLeases error
// that is not a Conflict keeps the reservation held and tracked, is not
// returned, and is not counted as renewed; the next clean tick renews it.
func TestHeartbeatTransientRenewRefusalIsNotAnError(t *testing.T) {
	f := newReserverFixture(t, 100000)
	busy := true
	f.seams.RenewLeases = func(context.Context, []string) error {
		if busy {
			return cascade.New(cascade.KindUnavailable, "lease table busy")
		}
		return nil
	}
	rv := f.reserver(t)
	r, err := rv.Reserve(t.Context(), baseReserveRequest())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if renewed, lost, err := rv.Heartbeat(t.Context()); err != nil || renewed != 0 || lost != 0 {
		t.Fatalf("Heartbeat = %d, %d, %v, want 0, 0, nil (an unrenewed lease is not renewed)", renewed, lost, err)
	}
	if s, _, _ := f.store.Get(t.Context(), r.ID); s.State != ReservationHeld || !rv.isTracked(r.ID) {
		t.Fatalf("state = %s tracked=%v, want held and still tracked", s.State, rv.isTracked(r.ID))
	}
	busy = false
	if renewed, lost, err := rv.Heartbeat(t.Context()); err != nil || renewed != 1 || lost != 0 {
		t.Fatalf("clean Heartbeat = %d, %d, %v, want 1, 0, nil", renewed, lost, err)
	}
}

// TestHeartbeatLostLeaseRetiresReservation: a fenced lease (Conflict)
// rolls the reservation back through its reverse teardown, removes it
// from the in-memory set, raises one lost_lease attention and counts it,
// while Heartbeat returns nil; a failing RaiseAttention still retires the
// row and is returned after every id is processed; ExpireStale still runs.
func TestHeartbeatLostLeaseRetiresReservation(t *testing.T) {
	f := newReserverFixture(t, 100000)
	fenced := cascade.New(cascade.KindConflict, "jobs: lease fenced")
	f.seams.RenewLeases = func(context.Context, []string) error { return fenced }
	rv := f.reserver(t)
	r, err := rv.Reserve(t.Context(), baseReserveRequest())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	f.rec.logs = nil
	renewed, lost, err := rv.Heartbeat(t.Context())
	if err != nil || lost != 1 || renewed != 0 {
		t.Fatalf("Heartbeat = %d, %d, %v, want 0 renewed, 1 lost, nil error", renewed, lost, err)
	}
	if got := fmt.Sprint(f.rec.snapshot()); got != "[remove_worktree release_leases attention:lost_lease]" {
		t.Fatalf("calls = %s, want the reverse teardown then one lost_lease attention", got)
	}
	if s, _, _ := f.store.Get(t.Context(), r.ID); s.State != ReservationRolledBack {
		t.Fatalf("state = %s, want rolled_back", s.State)
	}
	if renewed, lost, err := rv.Heartbeat(t.Context()); renewed != 0 || lost != 0 || err != nil {
		t.Fatalf("second Heartbeat = %d, %d, %v, want the id gone from the set", renewed, lost, err)
	}

	notify := errors.New("attention store down")
	f.seams.RaiseAttention = func(context.Context, string, string) error { return notify }
	rv2 := f.reserverWithEpoch(t, "epoch-2")
	a, _ := rv2.Reserve(t.Context(), baseReserveRequest())
	b, _ := rv2.Reserve(t.Context(), baseReserveRequest())
	_, lost, err = rv2.Heartbeat(t.Context())
	if lost != 2 || !hasIdentity(err, notify) {
		t.Fatalf("Heartbeat with failing attention = lost %d err %v, want 2 and the attention error", lost, err)
	}
	for _, id := range []string{a.ID, b.ID} {
		if s, _, _ := f.store.Get(t.Context(), id); s.State != ReservationRolledBack {
			t.Errorf("%s state = %s, want rolled_back despite the attention failure", id, s.State)
		}
	}
	insertUnheld(t, f, "r-stale", ReservationHeld, 0)
	f.clock.Advance(4 * hbInterval)
	if n, err := rv2.ExpireStale(t.Context(), hbInterval); err != nil || n != 1 {
		t.Fatalf("ExpireStale after lost leases = %d, %v, want 1", n, err)
	}
}

// TestReserverAdopt: Adopt on an adopted committed row rewrites owner
// and heartbeat and adds it to the set, so ExpireStale then skips it;
// Adopt on a terminal row refuses.
func TestReserverAdopt(t *testing.T) {
	f := newReserverFixture(t, 100000)
	insertUnheld(t, f, "r-commit", ReservationCommitted, f.clock.Now().Unix())
	insertUnheld(t, f, "r-done", ReservationReleased, f.clock.Now().Unix())
	rv := f.reserverWithEpoch(t, "epoch-2")
	if rep, err := rv.Sweep(t.Context(), func(context.Context) (bool, error) { return false, nil }); err != nil || rep.Adopted != 1 {
		t.Fatalf("Sweep = %+v, %v, want the committed row adopted", rep, err)
	}
	f.clock.Advance(hbInterval)
	if err := rv.Adopt(t.Context(), "r-commit"); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	got, _, _ := f.store.Get(t.Context(), "r-commit")
	if got.OwnerEpoch != "epoch-2" || got.HeartbeatAt != f.clock.Now().Unix() {
		t.Fatalf("adopted row = %+v, want epoch-2 and a fresh heartbeat", got)
	}
	f.clock.Advance(4 * hbInterval)
	if n, err := rv.ExpireStale(t.Context(), hbInterval); err != nil || n != 0 {
		t.Fatalf("ExpireStale = %d, %v, want the adopted row skipped", n, err)
	}
	requireSentinel(t, rv.Adopt(t.Context(), "r-done"), ErrReservationTransition, "released")
	insertUnheld(t, f, "r-live-other", ReservationCommitted, f.clock.Now().Unix())
	if err := rv.Adopt(t.Context(), "r-live-other"); !cascade.HasKind(err, cascade.KindConflict) || !strings.Contains(err.Error(), "epoch-dead") {
		t.Errorf("Adopt of a row another epoch owns = %v, want KindConflict naming that epoch", err)
	}
	if other, _, _ := f.store.Get(t.Context(), "r-live-other"); other.OwnerEpoch != "epoch-dead" {
		t.Errorf("refused Adopt rewrote owner_epoch to %q", other.OwnerEpoch)
	}
	if err := rv.Adopt(t.Context(), "never-created"); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Errorf("Adopt(missing) = %v, want KindNotFound", err)
	}
}

// TestAdoptDuringExpireStale: Adopt runs from inside the RemoveWorktree
// seam while ExpireStale is retiring one of two adopted-by-sweep rows.
// Adopting the row being retired refuses (its teardown already started),
// so it ends released and untracked; adopting the other row wins, so
// ExpireStale skips it and it stays committed, tracked, worktree intact.
// Never both: an Adopt that succeeded is never followed by a retirement.
func TestAdoptDuringExpireStale(t *testing.T) {
	f := newReserverFixture(t, 100000)
	insertUnheld(t, f, "r-a", ReservationCommitted, f.clock.Now().Unix())
	insertUnheld(t, f, "r-b", ReservationCommitted, f.clock.Now().Unix())
	var rv *Reserver
	adopted := map[string]error{}
	var removed []string
	f.seams.RemoveWorktree = func(ctx context.Context, wt string) error {
		removed = append(removed, wt)
		if len(removed) == 1 {
			self := strings.TrimSuffix(wt, "-wt")
			other := map[string]string{"r-a": "r-b", "r-b": "r-a"}[self]
			adopted[self], adopted[other] = rv.Adopt(ctx, self), rv.Adopt(ctx, other)
		}
		return nil
	}
	rv = f.reserverWithEpoch(t, "epoch-2")
	if rep, err := rv.Sweep(t.Context(), notLive); err != nil || rep.Adopted != 2 {
		t.Fatalf("Sweep = %+v, %v, want both committed rows adopted", rep, err)
	}
	f.clock.Advance(4 * hbInterval)
	n, err := rv.ExpireStale(t.Context(), hbInterval)
	if err != nil || n != 1 || len(removed) != 1 {
		t.Fatalf("ExpireStale = %d, %v, worktrees removed %v, want exactly one row retired", n, err, removed)
	}
	self := strings.TrimSuffix(removed[0], "-wt")
	other := map[string]string{"r-a": "r-b", "r-b": "r-a"}[self]
	requireSentinel(t, adopted[self], ErrReservationTransition, "expir")
	if s, _, _ := f.store.Get(t.Context(), self); s.State != ReservationReleased || rv.isTracked(self) {
		t.Fatalf("retired row = %s tracked=%v, want released and untracked", s.State, rv.isTracked(self))
	}
	if adopted[other] != nil {
		t.Fatalf("Adopt(%s) before its turn = %v, want nil", other, adopted[other])
	}
	if s, _, _ := f.store.Get(t.Context(), other); s.State != ReservationCommitted || s.WorktreeID != other+"-wt" || !rv.isTracked(other) {
		t.Fatalf("adopted row = %+v tracked=%v, want committed, tracked, worktree intact", s, rv.isTracked(other))
	}
}
