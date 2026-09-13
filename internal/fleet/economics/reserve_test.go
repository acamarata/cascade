package economics

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/governor"
)

// callRecorder is a race-safe ordered call log every stub seam in this
// file appends to, so a test can assert the EXACT order acquisitions and
// compensations happened in, not merely that they happened.
type callRecorder struct {
	mu   sync.Mutex
	logs []string
}

func (r *callRecorder) record(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, s)
}

func (r *callRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.logs))
	copy(out, r.logs)
	return out
}

// reserverFixture bundles a Reserver over a real test db with every seam
// stubbed to record its call and, by default, succeed. Tests override
// individual seam funcs (via the returned struct's exported fields
// before constructing the Reserver, or by re-deriving) to inject
// failures at a specific step.
type reserverFixture struct {
	store     *ReservationStore
	rec       *callRecorder
	capacity  int64
	committed int64

	buckets          DomainBucketsFn
	permit           PermitFn
	acquireLeases    AcquireLeasesFn
	releaseLeases    ReleaseLeasesFn
	allocateWorktree AllocateWorktreeFn
	removeWorktree   RemoveWorktreeFn
	validateLeases   ValidateLeasesFn
}

func newReserverFixture(t *testing.T, capacity int64) *reserverFixture {
	t.Helper()
	f := &reserverFixture{store: newReservationTestStore(t), rec: &callRecorder{}, capacity: capacity}
	f.buckets = func(_ context.Context, _ string) (map[string]Bucket, error) {
		return map[string]Bucket{"combined": {CapacityObserved: f.capacity, CommittedSinceObservation: f.committed}}, nil
	}
	f.permit = func(_ context.Context, _ governor.AdmissionRequest) (governor.Permit, error) {
		f.rec.record("permit")
		return governor.Permit{}, nil
	}
	f.acquireLeases = func(_ context.Context, _ string, _ []string) ([]string, error) {
		f.rec.record("acquire_leases")
		return []string{"lease-1", "lease-2"}, nil
	}
	f.releaseLeases = func(_ context.Context, _ []string) error {
		f.rec.record("release_leases")
		return nil
	}
	f.allocateWorktree = func(_ context.Context, _ string) (string, error) {
		f.rec.record("allocate_worktree")
		return "worktree-1", nil
	}
	f.removeWorktree = func(_ context.Context, _ string) error {
		f.rec.record("remove_worktree")
		return nil
	}
	f.validateLeases = func(_ context.Context, _ string, leaseIDs []string) ([]string, error) {
		return leaseIDs, nil
	}
	return f
}

func (f *reserverFixture) reserver(t *testing.T) *Reserver {
	t.Helper()
	rv, err := NewReserver(f.store, newTestClock(), "epoch-1", f.buckets, f.permit,
		f.acquireLeases, f.releaseLeases, f.allocateWorktree, f.removeWorktree, f.validateLeases)
	if err != nil {
		t.Fatalf("NewReserver: %v", err)
	}
	return rv
}

func TestNewReserverRefusesNilSeams(t *testing.T) {
	f := newReserverFixture(t, 1000)
	cases := []struct {
		name   string
		mutate func(*reserverFixture)
	}{
		{"store", func(_ *reserverFixture) {}}, // handled separately below
		{"buckets", func(f *reserverFixture) { f.buckets = nil }},
		{"permit", func(f *reserverFixture) { f.permit = nil }},
		{"acquireLeases", func(f *reserverFixture) { f.acquireLeases = nil }},
		{"releaseLeases", func(f *reserverFixture) { f.releaseLeases = nil }},
		{"allocateWorktree", func(f *reserverFixture) { f.allocateWorktree = nil }},
		{"removeWorktree", func(f *reserverFixture) { f.removeWorktree = nil }},
		{"validateLeases", func(f *reserverFixture) { f.validateLeases = nil }},
	}
	if _, err := NewReserver(nil, newTestClock(), "epoch-1", f.buckets, f.permit, f.acquireLeases, f.releaseLeases, f.allocateWorktree, f.removeWorktree, f.validateLeases); err == nil {
		t.Error("NewReserver(nil store) = nil error, want error")
	}
	for _, c := range cases[1:] {
		fx := newReserverFixture(t, 1000)
		c.mutate(fx)
		_, err := NewReserver(fx.store, newTestClock(), "epoch-1", fx.buckets, fx.permit, fx.acquireLeases, fx.releaseLeases, fx.allocateWorktree, fx.removeWorktree, fx.validateLeases)
		if err == nil {
			t.Errorf("NewReserver(nil %s) = nil error, want error", c.name)
		}
	}
}

func TestReserveHappyPathInteractive(t *testing.T) {
	f := newReserverFixture(t, 100000)
	rv := f.reserver(t)
	r, err := rv.Reserve(t.Context(), baseReserveRequest())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if r.State != ReservationHeld {
		t.Errorf("state = %s, want held", r.State)
	}
	if len(r.LeaseIDs) != 2 || r.WorktreeID != "worktree-1" {
		t.Errorf("Reserve did not persist lease_ids/worktree_id: %+v", r)
	}
	if got := f.rec.snapshot(); len(got) != 2 || got[0] != "acquire_leases" || got[1] != "allocate_worktree" {
		t.Errorf("call order = %v, want [acquire_leases allocate_worktree]", got)
	}
	if len(r.Steps) != 3 {
		t.Fatalf("Steps = %+v, want exactly 3 (quota, leases, worktree)", r.Steps)
	}
	for _, want := range []StepName{StepQuota, StepLeases, StepWorktree} {
		found := false
		for _, s := range r.Steps {
			if s.Step == want && s.State == StepAcquired {
				found = true
			}
		}
		if !found {
			t.Errorf("Steps missing acquired %s: %+v", want, r.Steps)
		}
	}
	stored, ok, err := f.store.Get(t.Context(), r.ID)
	if err != nil || !ok {
		t.Fatalf("Get: %v %v", ok, err)
	}
	if stored.State != ReservationHeld {
		t.Errorf("persisted state = %s, want held", stored.State)
	}
}

func TestReserveBatchSkipsLeasesAndWorktree(t *testing.T) {
	f := newReserverFixture(t, 100000)
	rv := f.reserver(t)
	req := baseReserveRequest()
	req.Kind = ReservationBatch
	req.ScopeGlobs = nil
	r, err := rv.Reserve(t.Context(), req)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if len(r.LeaseIDs) != 0 || r.WorktreeID != "" {
		t.Errorf("batch reservation acquired leases/worktree: %+v", r)
	}
	if got := f.rec.snapshot(); len(got) != 0 {
		t.Errorf("batch reservation invoked seams: %v, want none", got)
	}
	if r.ExpiresAt == 0 {
		t.Error("batch reservation ExpiresAt not set (want 24h expiry)")
	}
}

func TestReserveBatchRejectsScopeGlobs(t *testing.T) {
	f := newReserverFixture(t, 100000)
	rv := f.reserver(t)
	req := baseReserveRequest()
	req.Kind = ReservationBatch
	req.ScopeGlobs = []string{"**"}
	if _, err := rv.Reserve(t.Context(), req); err == nil {
		t.Error("Reserve(batch with ScopeGlobs) = nil error, want typed error")
	}
}

// TestReserveFailureAtQuotaRollsBackNothingAcquired proves the FIRST
// failure point (admission) rolls back with zero acquisitions attempted
// -- the "no residue" case: leases and worktree seams are never called,
// and the row ends rolled_back.
func TestReserveFailureAtQuotaRollsBackNothingAcquired(t *testing.T) {
	f := newReserverFixture(t, 1) // capacity far below any real estimate
	rv := f.reserver(t)
	r, err := rv.Reserve(t.Context(), baseReserveRequest())
	if err == nil {
		t.Fatal("Reserve over capacity = nil error, want ErrQuotaUnavailable")
	}
	if !errors.Is(err, ErrQuotaUnavailable) {
		t.Errorf("err = %v, want ErrQuotaUnavailable", err)
	}
	if r.State != ReservationRolledBack {
		t.Errorf("state = %s, want rolled_back", r.State)
	}
	if got := f.rec.snapshot(); len(got) != 0 {
		t.Errorf("call order = %v, want none (nothing acquired before quota check)", got)
	}
	if len(r.LeaseIDs) != 0 || r.WorktreeID != "" {
		t.Errorf("rolled-back row carries residue: %+v", r)
	}
}

// TestReserveFailureAtLeasesRollsBack injects a failure at the SECOND
// pipeline step (leases). Nothing was acquired before it, so the
// reverse chain has nothing to compensate; worktree is never attempted.
func TestReserveFailureAtLeasesRollsBack(t *testing.T) {
	f := newReserverFixture(t, 100000)
	injected := errors.New("lease acquisition failed")
	f.acquireLeases = func(_ context.Context, _ string, _ []string) ([]string, error) {
		f.rec.record("acquire_leases")
		return nil, injected
	}
	rv := f.reserver(t)
	r, err := rv.Reserve(t.Context(), baseReserveRequest())
	if !errors.Is(err, injected) {
		t.Errorf("err = %v, want wrapping %v", err, injected)
	}
	if r.State != ReservationRolledBack {
		t.Errorf("state = %s, want rolled_back", r.State)
	}
	if got := f.rec.snapshot(); len(got) != 1 || got[0] != "acquire_leases" {
		t.Errorf("call order = %v, want [acquire_leases] only (worktree never attempted)", got)
	}
}

// TestReserveFailureAtWorktreeRollsBackLeasesInReverseOrder is the
// dedicated per-step test for the THIRD pipeline step: leases succeed,
// worktree allocation fails. The reverse chain must release the leases
// that WERE acquired (removeWorktree is never called, since no worktree
// was ever allocated) and the row must read rolled_back.
func TestReserveFailureAtWorktreeRollsBackLeasesInReverseOrder(t *testing.T) {
	f := newReserverFixture(t, 100000)
	injected := errors.New("worktree allocation failed")
	f.allocateWorktree = func(_ context.Context, _ string) (string, error) {
		f.rec.record("allocate_worktree")
		return "", injected
	}
	rv := f.reserver(t)
	r, err := rv.Reserve(t.Context(), baseReserveRequest())
	if !errors.Is(err, injected) {
		t.Errorf("err = %v, want wrapping %v", err, injected)
	}
	if r.State != ReservationRolledBack {
		t.Errorf("state = %s, want rolled_back", r.State)
	}
	want := []string{"acquire_leases", "allocate_worktree", "release_leases"}
	if got := f.rec.snapshot(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("call order = %v, want %v (remove_worktree must NOT appear -- nothing was allocated)", got, want)
	}
	if len(r.LeaseIDs) != 0 {
		t.Errorf("rolled-back row still carries lease_ids: %+v", r)
	}
	// Step ledger must record the compensation.
	found := false
	for _, s := range r.Steps {
		if s.Step == StepLeases && s.State == StepCompensated {
			found = true
		}
	}
	if !found {
		t.Errorf("steps ledger missing compensated leases step: %+v", r.Steps)
	}
}
