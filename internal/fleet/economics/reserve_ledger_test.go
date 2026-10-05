package economics

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestReserveOrderAndHeldFirst: the held row (with its ExecutionID and a
// pending quota step) is durable before the quota check reads a bucket;
// acquisition order is quota, leases, worktree; an interactive request
// with zero ScopeGlobs takes no lease and no worktree step; a batch
// request with ScopeGlobs refuses before any row is written.
func TestReserveOrderAndHeldFirst(t *testing.T) {
	f := newReserverFixture(t, 100000)
	req := baseReserveRequest()
	current := req.ExecutionID
	f.seams.Buckets = func(ctx context.Context, domainID string) (topology.QuotaDomainKind, topology.LimitScopeID, map[string]topology.Bucket, error) {
		row, ok, err := f.store.GetByExecution(ctx, current)
		if err != nil || !ok || row.State != ReservationHeld || fmt.Sprint(row.Steps) != fmt.Sprintf("[{quota %s:quota  pending}]", row.ID) {
			t.Errorf("at the quota check the row is %+v ok=%v err=%v, want a durable held row with one pending quota step", row, ok, err)
		}
		f.rec.record("quota")
		return f.fixtureBuckets(ctx, domainID)
	}
	rv := f.reserver(t)
	if _, err := rv.Reserve(t.Context(), req); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if got := fmt.Sprint(f.rec.snapshot()); got != "[quota acquire_leases allocate_worktree]" {
		t.Fatalf("order = %s, want [quota acquire_leases allocate_worktree]", got)
	}

	quotaOnly := baseReserveRequest()
	quotaOnly.ScopeGlobs, current = nil, quotaOnly.ExecutionID
	r, err := rv.Reserve(t.Context(), quotaOnly)
	if err != nil || len(r.Steps) != 1 || r.Steps[0].Step != StepQuota || r.Steps[0].State != StepAcquired {
		t.Fatalf("interactive zero-glob Reserve = %+v, %v, want exactly one acquired quota step", r, err)
	}
	if got := fmt.Sprint(f.rec.snapshot()); got != "[quota acquire_leases allocate_worktree quota]" {
		t.Fatalf("zero-glob request ran %s, want no lease or worktree call", got)
	}

	batch := baseReserveRequest()
	batch.Kind = ReservationBatch
	if _, err := rv.Reserve(t.Context(), batch); !isKindInvalidInput(err) {
		t.Fatalf("batch with ScopeGlobs = %v, want KindInvalidInput", err)
	}
	if _, ok, _ := f.store.GetByExecution(t.Context(), batch.ExecutionID); ok {
		t.Error("a refused batch request left a row")
	}
}

// TestReserveDuplicateExecutionRefused: a second Reserve with the same
// ExecutionID is refused by the unique index before any lease or
// worktree is taken, and the first row is untouched.
func TestReserveDuplicateExecutionRefused(t *testing.T) {
	f := newReserverFixture(t, 100000)
	rv := f.reserver(t)
	req := baseReserveRequest()
	first, err := rv.Reserve(t.Context(), req)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	calls := len(f.rec.snapshot())
	if _, err := rv.Reserve(t.Context(), req); !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("duplicate Reserve = %v, want KindConflict", err)
	}
	if len(f.rec.snapshot()) != calls {
		t.Errorf("duplicate Reserve ran seams: %v", f.rec.snapshot())
	}
	stored, _, _ := f.store.GetByExecution(t.Context(), req.ExecutionID)
	if fmt.Sprint(stored) != fmt.Sprint(first) {
		t.Errorf("first row changed after a duplicate: %+v", stored)
	}
}

// raceReserve releases n Reserve calls through one barrier and counts
// the held results and the refusals carrying sentinel by identity.
func raceReserve(t *testing.T, rv *Reserver, n int, build func(i int) ReserveRequest, sentinel error) (held, refused int) {
	t.Helper()
	barrier := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]Reservation, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := build(i)
			<-barrier
			results[i], errs[i] = rv.Reserve(context.Background(), req)
		}(i)
	}
	close(barrier)
	wg.Wait()
	for i := 0; i < n; i++ {
		switch {
		case errs[i] == nil && results[i].State == ReservationHeld:
			held++
		case hasIdentity(errs[i], sentinel) && results[i].State == ReservationRolledBack:
			refused++
		default:
			t.Errorf("call %d: r=%+v err=%v, neither held nor refused by %v", i, results[i], errs[i], sentinel)
		}
	}
	return held, refused
}

// TestReserveConcurrentAdmissionBarrier: 32 goroutines against capacity
// for exactly 5 requests in one dimension admit exactly 5; the same
// shape against a project share admits exactly the share.
func TestReserveConcurrentAdmissionBarrier(t *testing.T) {
	f := newReserverFixture(t, 1000000)
	f.seams.Buckets = func(ctx context.Context, d string) (topology.QuotaDomainKind, topology.LimitScopeID, map[string]topology.Bucket, error) {
		kind, scope, b, err := f.fixtureBuckets(ctx, d)
		b[topology.DimensionRPM] = topology.Bucket{Name: topology.DimensionRPM, Limit: 5, LimitScopeID: scope, CapacityObserved: 5}
		return kind, scope, b, err
	}
	rv := f.reserver(t)
	held, refused := raceReserve(t, rv, 32, func(i int) ReserveRequest {
		r := baseReserveRequest()
		r.ScopeGlobs, r.ProjectID, r.Estimate = nil, fmt.Sprintf("project-%d", i), Estimate{TokensIn: 1, Requests: 1}
		return r
	}, ErrQuotaUnavailable)
	if held != 5 || refused != 27 {
		t.Fatalf("dimension barrier: held=%d refused=%d, want 5 and 27", held, refused)
	}

	g := newReserverFixture(t, 1000000)
	g.seams.ReserveFraction = func(context.Context, string) (float64, error) { return 0.5, nil }
	g.seams.Buckets = func(ctx context.Context, d string) (topology.QuotaDomainKind, topology.LimitScopeID, map[string]topology.Bucket, error) {
		kind, scope, b, err := g.fixtureBuckets(ctx, d)
		b[topology.DimensionRPD] = topology.Bucket{Name: topology.DimensionRPD, Limit: 100, LimitScopeID: scope, CapacityObserved: 100}
		return kind, scope, b, err
	}
	rv2 := g.reserver(t)
	held, refused = raceReserve(t, rv2, 32, func(int) ReserveRequest {
		r := baseReserveRequest()
		r.ScopeGlobs, r.Estimate = nil, Estimate{TokensIn: 1, Requests: 10}
		return r
	}, ErrProjectShareExceeded)
	if held != 5 || refused != 27 {
		t.Fatalf("project share barrier: held=%d refused=%d, want 5 (share 50 of 100) and 27", held, refused)
	}
}

// crashCase names one injected crash site and what the child's effects
// must look like afterwards.
type crashCase struct {
	site                string
	acquires, allocates int
	leases, worktree    StepState // step state after RecoverSteps ("" = absent)
}

// TestReserveStepLedgerRecovery: a child crashes (exit 3) after each
// step's pending entry and after each acquisition; RecoverSteps in a
// fresh Reserver re-queries FindLeases/FindWorktree with the persisted
// repo_id, job_id, scope_globs and lease id, records what it finds, and
// the sweep compensates it. Every effect ran exactly once.
func TestReserveStepLedgerRecovery(t *testing.T) {
	for _, c := range []crashCase{
		{site: "quota_pending"},
		{site: "leases_pending", leases: StepNotRun},
		{site: "leases_acquired", acquires: 1, leases: StepAcquired},
		{site: "worktree_pending", acquires: 1, leases: StepAcquired, worktree: StepNotRun},
		{site: "worktree_acquired", acquires: 1, allocates: 1, leases: StepAcquired, worktree: StepAcquired},
	} {
		t.Run(c.site, func(t *testing.T) { runCrashCase(t, c) })
	}
}

func runCrashCase(t *testing.T, c crashCase) {
	dir := t.TempDir()
	dbPath, fakePath := filepath.Join(dir, "reservation.db"), filepath.Join(dir, "subsystems.json")
	store, err := NewReservationStore(openReservationTestDBAt(t, dbPath), newTestClock())
	if err != nil {
		t.Fatalf("NewReservationStore: %v", err)
	}
	runCrashChild(t, dbPath, fakePath, c.site)
	held, err := store.ListByState(t.Context(), ReservationHeld)
	if err != nil || len(held) != 1 || held[0].OwnerEpoch != "epoch-child" {
		t.Fatalf("after the crash: %+v, %v, want exactly one held row of the dead child", held, err)
	}
	fake := &durableFake{path: fakePath}
	rv, err := NewReserver(store, newTestClock(), "epoch-parent", fake.seams())
	if err != nil {
		t.Fatalf("NewReserver: %v", err)
	}
	rec, err := rv.RecoverSteps(t.Context(), held[0].ID)
	if err != nil || findStep(rec, StepLeases).State != c.leases || findStep(rec, StepWorktree).State != c.worktree || findStep(rec, StepQuota).State == StepPending {
		t.Fatalf("RecoverSteps = %+v, %v, want leases %q worktree %q and no pending step", rec.Steps, err, c.leases, c.worktree)
	}
	if c.leases == StepAcquired && fmt.Sprint(rec.LeaseIDs) != "[L1]" {
		t.Fatalf("recovered lease ids = %v, want the child's L1 found by the persisted repo, job and globs", rec.LeaseIDs)
	}
	if rep, err := rv.Sweep(t.Context(), notLive); err != nil || rep.RolledBack != 1 {
		t.Fatalf("Sweep = %+v, %v, want the row rolled back", rep, err)
	}
	st := fake.update(func(*fakeState) {})
	release, remove := 0, 0
	if c.acquires > 0 {
		release = 1
	}
	if c.allocates > 0 {
		remove = 1
	}
	want := fmt.Sprint(c.acquires, c.allocates, release, remove)
	if got := fmt.Sprint(st.Calls["acquire"], st.Calls["allocate"], st.Calls["release"], st.Calls["remove"]); got != want {
		t.Fatalf("acquire/allocate/release/remove calls = %s, want %s (each effect exactly once)", got, want)
	}
	for id, l := range st.Leases {
		if l.Live {
			t.Errorf("lease %s still live after recovery", id)
		}
	}
	for p, w := range st.Worktrees {
		if w.Live {
			t.Errorf("worktree %s still live after recovery", p)
		}
	}
}
