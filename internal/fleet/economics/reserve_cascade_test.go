package economics

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/topology"
)

// TestReserveCascadeRateLimited: CascadeRateLimited(scope) parks every
// held row on that scope and returns the count; committed rows and rows
// on another scope are untouched; a parked row holds no permit.
func TestReserveCascadeRateLimited(t *testing.T) {
	f := newReserverFixture(t, 100000)
	rv := f.reserver(t)
	a, errA := rv.Reserve(t.Context(), baseReserveRequest())
	b, errB := rv.Reserve(t.Context(), baseReserveRequest())
	c, errC := rv.Reserve(t.Context(), baseReserveRequest())
	if errA != nil || errB != nil || errC != nil {
		t.Fatalf("Reserve: %v %v %v", errA, errB, errC)
	}
	if _, err := rv.Commit(t.Context(), c.ID); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	other := baseReservation("r-other-scope")
	other.ScopeID = "scope-2"
	if _, err := f.store.Insert(t.Context(), other); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	f.rec.logs = nil
	n, err := rv.CascadeRateLimited(t.Context(), "scope-1")
	if err != nil || n != 2 {
		t.Fatalf("CascadeRateLimited = %d, %v, want 2", n, err)
	}
	for id, want := range map[string]ReservationState{a.ID: ReservationParked, b.ID: ReservationParked, c.ID: ReservationCommitted, "r-other-scope": ReservationHeld} {
		if r, _, _ := f.store.Get(t.Context(), id); r.State != want {
			t.Errorf("%s = %s, want %s", id, r.State, want)
		}
	}
	err = rv.WithPermit(t.Context(), a.ID, func(context.Context) error {
		t.Fatal("fn ran for a parked reservation")
		return nil
	})
	requireSentinel(t, err, ErrReservationTransition, "parked")
	if calls := f.rec.snapshot(); len(calls) != 0 {
		t.Fatalf("cascade or parked permit ran seams: %v", calls)
	}
	if again, err := rv.CascadeRateLimited(t.Context(), "scope-1"); err != nil || again != 0 {
		t.Errorf("second cascade = %d, %v, want 0", again, err)
	}
	if _, err := rv.CascadeRateLimited(t.Context(), ""); !isKindInvalidInput(err) {
		t.Errorf("CascadeRateLimited(empty scope) = %v, want KindInvalidInput", err)
	}
}

// TestCascadeDuringAcquisitionParksOnCompletion: a cascade that lands
// while Reserve is still acquiring (here, inside AllocateWorktree) does
// not park the row under the acquisition's feet. The row is parked the
// moment its acquisition finishes, with every handle kept; the cascade
// does not count it, because no park of it had landed when it returned.
func TestCascadeDuringAcquisitionParksOnCompletion(t *testing.T) {
	f := newReserverFixture(t, 100000)
	var rv *Reserver
	var cascaded int
	var cascadeErr error
	f.seams.AllocateWorktree = func(ctx context.Context, _ string) (string, error) {
		cascaded, cascadeErr = rv.CascadeRateLimited(ctx, "scope-1")
		return "worktree-1", nil
	}
	rv = f.reserver(t)
	r, err := rv.Reserve(t.Context(), baseReserveRequest())
	if err != nil || r.State != ReservationParked {
		t.Fatalf("Reserve = %+v, %v, want the row parked after its acquisition", r, err)
	}
	if cascadeErr != nil || cascaded != 0 {
		t.Fatalf("CascadeRateLimited mid-acquisition = %d, %v, want 0 (deferred), nil", cascaded, cascadeErr)
	}
	stored, _, _ := f.store.Get(t.Context(), r.ID)
	if stored.State != ReservationParked || stored.WorktreeID != "worktree-1" || len(stored.LeaseIDs) != 2 || !rv.isTracked(r.ID) {
		t.Fatalf("stored = %+v tracked=%v, want parked, tracked, leases and worktree kept", stored, rv.isTracked(r.ID))
	}
	for _, c := range f.rec.snapshot() {
		if c == "remove_worktree" || c == "release_leases" {
			t.Fatalf("calls = %v, want no compensation", f.rec.snapshot())
		}
	}
}

// bindBeforeWrite makes the store run one Bind of id at the start of the
// next write to id: after the writer's read, before its write lands.
func bindBeforeWrite(f *reserverFixture, rv *Reserver, id string) *error {
	var bindErr error
	fired := false
	f.store.beforeWrite = func(ctx context.Context, got string) {
		if got == id && !fired {
			fired = true
			_, bindErr = rv.Bind(ctx, id, testPlacement, writeCallerRow)
		}
	}
	return &bindErr
}

// requirePlacementKept asserts id is in state want with the Bind's
// placement intact and the caller's decision row committed.
func requirePlacementKept(t *testing.T, f *reserverFixture, id string, want ReservationState) {
	t.Helper()
	got, _, _ := f.store.Get(t.Context(), id)
	if got.State != want || got.NodeID != testPlacement.NodeID || got.SelectedTier != testPlacement.SelectedTier ||
		got.Sensitivity != testPlacement.Sensitivity || got.DecisionID != testPlacement.DecisionID {
		t.Fatalf("row = %+v, want %s with the committed Bind's placement", got, want)
	}
	var node string
	err := f.store.DB().QueryRowContext(t.Context(), `SELECT node_id FROM caller_decision WHERE execution_id = ?`, got.ExecutionID).Scan(&node)
	if err != nil || node != testPlacement.NodeID {
		t.Fatalf("caller decision row = %q, %v, want the Bind's row", node, err)
	}
}

// TestStateWritesKeepConcurrentBind: a Bind that commits after a writer
// read the row and before its write lands survives every state write:
// the cascade park, Park, Commit and Release. Each landed its own move.
func TestStateWritesKeepConcurrentBind(t *testing.T) {
	cases := []struct {
		name string
		op   func(t *testing.T, rv *Reserver, id string)
		want ReservationState
	}{
		{"cascade", func(t *testing.T, rv *Reserver, _ string) {
			if n, err := rv.CascadeRateLimited(t.Context(), "scope-1"); err != nil || n != 1 {
				t.Fatalf("CascadeRateLimited = %d, %v, want 1", n, err)
			}
		}, ReservationParked},
		{"park", func(t *testing.T, rv *Reserver, id string) {
			if _, err := rv.Park(t.Context(), id); err != nil {
				t.Fatalf("Park: %v", err)
			}
		}, ReservationParked},
		{"commit", func(t *testing.T, rv *Reserver, id string) {
			if _, err := rv.Commit(t.Context(), id); err != nil {
				t.Fatalf("Commit: %v", err)
			}
		}, ReservationCommitted},
		{"release", func(t *testing.T, rv *Reserver, id string) {
			if _, err := rv.Release(t.Context(), id); err != nil {
				t.Fatalf("Release: %v", err)
			}
		}, ReservationReleased},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, rv, r := bindFixture(t)
			bindErr := bindBeforeWrite(f, rv, r.ID)
			c.op(t, rv, r.ID)
			if *bindErr != nil {
				t.Fatalf("Bind inside the write window: %v", *bindErr)
			}
			requirePlacementKept(t, f, r.ID, c.want)
		})
	}
}

// TestDeferredParkKeepsBindDuringAcquisition: a Bind and a cascade both
// land while Reserve is allocating the worktree; the progress writes and
// the deferred park that follow keep the placement.
func TestDeferredParkKeepsBindDuringAcquisition(t *testing.T) {
	f, _, _ := bindFixture(t)
	req := baseReserveRequest()
	var rv *Reserver
	var bindErr error
	f.seams.AllocateWorktree = func(ctx context.Context, _ string) (string, error) {
		held, _, _ := f.store.GetByExecution(ctx, req.ExecutionID)
		_, bindErr = rv.Bind(ctx, held.ID, testPlacement, writeCallerRow)
		_, _ = rv.CascadeRateLimited(ctx, "scope-1")
		return "worktree-1", nil
	}
	rv = f.reserver(t)
	r, err := rv.Reserve(t.Context(), req)
	if err != nil || bindErr != nil {
		t.Fatalf("Reserve = %v, Bind = %v", err, bindErr)
	}
	requirePlacementKept(t, f, r.ID, ReservationParked)
	if r.NodeID != testPlacement.NodeID {
		t.Fatalf("returned row = %+v, want the stored placement", r)
	}
}

// TestCascadeCountsOnlyLandedParks: a row that leaves held between the
// listing and the park write is neither parked nor counted nor an error.
func TestCascadeCountsOnlyLandedParks(t *testing.T) {
	f := newReserverFixture(t, 100000)
	rv := f.reserver(t)
	r, err := rv.Reserve(t.Context(), baseReserveRequest())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	fired := false
	f.store.beforeWrite = func(ctx context.Context, id string) {
		if id == r.ID && !fired {
			fired = true
			_, _ = rv.Commit(ctx, id)
		}
	}
	if n, err := rv.CascadeRateLimited(t.Context(), "scope-1"); err != nil || n != 0 {
		t.Fatalf("CascadeRateLimited over a row committed mid-park = %d, %v, want 0, nil", n, err)
	}
	if got, _, _ := f.store.Get(t.Context(), r.ID); got.State != ReservationCommitted {
		t.Fatalf("row = %s, want committed", got.State)
	}
}

// TestCascadeFlagOnFailedAcquisitionNotCounted: a cascade that flags a
// row mid-acquisition does not count it, and the acquisition then fails:
// the row ends rolled back, never parked.
func TestCascadeFlagOnFailedAcquisitionNotCounted(t *testing.T) {
	f := newReserverFixture(t, 100000)
	var rv *Reserver
	cause := errors.New("no worktree")
	cascaded := -1
	f.seams.AllocateWorktree = func(ctx context.Context, _ string) (string, error) {
		cascaded, _ = rv.CascadeRateLimited(ctx, "scope-1")
		return "", cause
	}
	rv = f.reserver(t)
	failed, err := rv.Reserve(t.Context(), baseReserveRequest())
	if !hasIdentity(err, cause) || failed.State != ReservationRolledBack || cascaded != 0 {
		t.Fatalf("Reserve = %s, %v; cascade counted %d, want rolled_back, the cause and 0", failed.State, err, cascaded)
	}
}

// reservePanics runs Reserve and returns the value it panicked with.
func reservePanics(ctx context.Context, rv *Reserver, req ReserveRequest) (p any) {
	defer func() { p = recover() }()
	_, _ = rv.Reserve(ctx, req)
	return nil
}

// TestPanickingSeamLeavesNoInflightMark: a seam that panics during
// admission or acquisition leaves no in-flight mark and no locked
// admission mutex, and its row is retired (rolled back, untracked, leases
// released), not stranded held and tracked.
func TestPanickingSeamLeavesNoInflightMark(t *testing.T) {
	for name, set := range map[string]func(f *reserverFixture){
		"buckets": func(f *reserverFixture) {
			f.seams.Buckets = func(context.Context, string) (topology.QuotaDomainKind, topology.LimitScopeID, map[string]topology.Bucket, error) {
				panic("seam exploded")
			}
		},
		"allocate_worktree": func(f *reserverFixture) {
			f.seams.AllocateWorktree = func(context.Context, string) (string, error) { panic("seam exploded") }
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newReserverFixture(t, 100000)
			set(f)
			rv := f.reserver(t)
			req := baseReserveRequest()
			if p := reservePanics(t.Context(), rv, req); p != "seam exploded" {
				t.Fatalf("Reserve panicked with %v, want the seam's panic", p)
			}
			rv.acqMu.Lock()
			inflight := len(rv.acquiring)
			rv.acqMu.Unlock()
			if inflight != 0 {
				t.Fatalf("acquiring holds %d ids after a panicking seam, want 0", inflight)
			}
			if !rv.mu.TryLock() {
				t.Fatal("admission mutex still locked after a panicking seam")
			}
			rv.mu.Unlock()
			r, ok, _ := f.store.GetByExecution(t.Context(), req.ExecutionID)
			if !ok || r.State != ReservationRolledBack || rv.isTracked(r.ID) || len(r.LeaseIDs) != 0 {
				t.Fatalf("row = %+v tracked=%v, want rolled_back, untracked, no leases", r, rv.isTracked(r.ID))
			}
		})
	}
}
