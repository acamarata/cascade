package economics

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/cascade"
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
// individual f.seams fields before calling f.reserver.
type reserverFixture struct {
	store     *ReservationStore
	rec       *callRecorder
	clock     *stepClock
	capacity  int64
	committed int64
	seams     ReserverSeams
}

// fixtureBuckets is the default Buckets seam: one api_project domain on
// scope-1 whose rpm/tpm/rpd buckets each hold f.capacity.
func (f *reserverFixture) fixtureBuckets(_ context.Context, _ string) (topology.QuotaDomainKind, topology.LimitScopeID, map[string]topology.Bucket, error) {
	out := make(map[string]topology.Bucket, 3)
	for _, dim := range []string{topology.DimensionRPM, topology.DimensionTPM, topology.DimensionRPD} {
		out[dim] = topology.Bucket{Name: dim, Limit: f.capacity, LimitScopeID: "scope-1", CapacityObserved: f.capacity, CommittedSinceObservation: f.committed}
	}
	return topology.QuotaDomainAPIProject, "scope-1", out, nil
}

func newReserverFixture(t *testing.T, capacity int64) *reserverFixture {
	t.Helper()
	f := &reserverFixture{store: newReservationTestStore(t), rec: &callRecorder{}, clock: newStepClock(), capacity: capacity}
	f.seams = ReserverSeams{
		Buckets:         f.fixtureBuckets,
		ReserveFraction: func(context.Context, string) (float64, error) { return 0, nil },
		Projects:        &ActiveProjectCount{},
		Permit: func(context.Context, governor.AdmissionRequest) (governor.Permit, error) {
			f.rec.record("permit")
			return governor.Permit{}, nil
		},
		AcquireLeases: func(context.Context, string, string, []string) ([]string, error) {
			f.rec.record("acquire_leases")
			return []string{"lease-1", "lease-2"}, nil
		},
		ReleaseLeases: func(context.Context, []string) error { f.rec.record("release_leases"); return nil },
		AllocateWorktree: func(context.Context, string) (string, error) {
			f.rec.record("allocate_worktree")
			return "worktree-1", nil
		},
		RemoveWorktree: func(context.Context, string) error { f.rec.record("remove_worktree"); return nil },
		ValidateLeases: func(_ context.Context, _ string, ids []string) ([]string, error) { return ids, nil },
		RenewLeases:    func(context.Context, []string) error { return nil },
		FindLeases:     func(context.Context, string, string, []string) ([]string, error) { return nil, nil },
		FindWorktree:   func(context.Context, string) (string, bool, error) { return "", false, nil },
		RaiseAttention: func(_ context.Context, _, reason string) error { f.rec.record("attention:" + reason); return nil },
	}
	return f
}

func (f *reserverFixture) reserverWithEpoch(t *testing.T, epoch string) *Reserver {
	t.Helper()
	rv, err := NewReserver(f.store, f.clock, epoch, f.seams)
	if err != nil {
		t.Fatalf("NewReserver: %v", err)
	}
	return rv
}

func (f *reserverFixture) reserver(t *testing.T) *Reserver {
	t.Helper()
	return f.reserverWithEpoch(t, "epoch-1")
}

func TestNewReserverRefusesNilSeams(t *testing.T) {
	f := newReserverFixture(t, 1000)
	if _, err := NewReserver(nil, f.clock, "epoch-1", f.seams); err == nil {
		t.Error("NewReserver(nil store) = nil error, want error")
	}
	if _, err := NewReserver(f.store, f.clock, "", f.seams); err == nil {
		t.Error("NewReserver(empty epoch) = nil error, want error")
	}
	cases := map[string]func(*ReserverSeams){
		"Buckets": func(s *ReserverSeams) { s.Buckets = nil }, "RenewLeases": func(s *ReserverSeams) { s.RenewLeases = nil },
		"Permit": func(s *ReserverSeams) { s.Permit = nil }, "AcquireLeases": func(s *ReserverSeams) { s.AcquireLeases = nil },
		"ReleaseLeases": func(s *ReserverSeams) { s.ReleaseLeases = nil }, "AllocateWorktree": func(s *ReserverSeams) { s.AllocateWorktree = nil },
		"RemoveWorktree": func(s *ReserverSeams) { s.RemoveWorktree = nil }, "ValidateLeases": func(s *ReserverSeams) { s.ValidateLeases = nil },
		"ReserveFraction": func(s *ReserverSeams) { s.ReserveFraction = nil }, "Projects": func(s *ReserverSeams) { s.Projects = nil },
		"FindLeases": func(s *ReserverSeams) { s.FindLeases = nil }, "FindWorktree": func(s *ReserverSeams) { s.FindWorktree = nil },
		"RaiseAttention": func(s *ReserverSeams) { s.RaiseAttention = nil },
	}
	for name, mutate := range cases {
		s := f.seams
		mutate(&s)
		_, err := NewReserver(f.store, f.clock, "epoch-1", s)
		if err == nil {
			t.Errorf("NewReserver(nil %s) = nil error, want KindInvalidInput", name)
			continue
		}
		if !isKindInvalidInput(err) || !strings.Contains(err.Error(), name) {
			t.Errorf("NewReserver(nil %s) = %v, want KindInvalidInput naming the seam", name, err)
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
	if r.State != ReservationHeld || len(r.LeaseIDs) != 2 || r.WorktreeID != "worktree-1" || r.ScopeID != "scope-1" {
		t.Errorf("Reserve = %+v, want held with leases, worktree and derived scope-1", r)
	}
	if got := f.rec.snapshot(); fmt.Sprint(got) != "[acquire_leases allocate_worktree]" {
		t.Errorf("call order = %v, want [acquire_leases allocate_worktree]", got)
	}
	if len(r.Steps) != 3 {
		t.Fatalf("Steps = %+v, want exactly 3 (quota, leases, worktree)", r.Steps)
	}
	for i, want := range []StepName{StepQuota, StepLeases, StepWorktree} {
		if r.Steps[i].Step != want || r.Steps[i].State != StepAcquired {
			t.Errorf("Steps[%d] = %+v, want acquired %s", i, r.Steps[i], want)
		}
	}
	stored, ok, err := f.store.Get(t.Context(), r.ID)
	if err != nil || !ok || stored.State != ReservationHeld || stored.RepoID != "repo-1" || fmt.Sprint(stored.ScopeGlobs) != "[**]" {
		t.Errorf("persisted = %+v ok=%v err=%v, want held with repo_id and scope_globs", stored, ok, err)
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
	if len(r.LeaseIDs) != 0 || r.WorktreeID != "" || len(f.rec.snapshot()) != 0 {
		t.Errorf("batch reservation acquired leases/worktree: %+v calls=%v", r, f.rec.snapshot())
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
	if _, err := rv.Reserve(t.Context(), req); !isKindInvalidInput(err) {
		t.Errorf("Reserve(batch with ScopeGlobs) = %v, want KindInvalidInput", err)
	}
}

// TestReserveFailureAtQuotaRollsBackNothingAcquired proves the FIRST
// failure point (admission) rolls back with zero acquisitions attempted.
func TestReserveFailureAtQuotaRollsBackNothingAcquired(t *testing.T) {
	f := newReserverFixture(t, 1) // capacity far below any real estimate
	rv := f.reserver(t)
	r, err := rv.Reserve(t.Context(), baseReserveRequest())
	requireSentinel(t, err, ErrQuotaUnavailable)
	if r.State != ReservationRolledBack {
		t.Errorf("state = %s, want rolled_back", r.State)
	}
	if got := f.rec.snapshot(); len(got) != 0 {
		t.Errorf("call order = %v, want none (nothing acquired before quota check)", got)
	}
	stored, ok, getErr := f.store.Get(t.Context(), r.ID)
	if getErr != nil || !ok || stored.State != ReservationRolledBack {
		t.Errorf("row after failed admission = %+v ok=%v err=%v, want a persisted rolled_back row", stored, ok, getErr)
	}
}

// TestReserveFailureAtLeasesRollsBack injects a failure at the leases
// step; worktree is never attempted.
func TestReserveFailureAtLeasesRollsBack(t *testing.T) {
	f := newReserverFixture(t, 100000)
	injected := errors.New("lease acquisition failed")
	f.seams.AcquireLeases = func(context.Context, string, string, []string) ([]string, error) {
		f.rec.record("acquire_leases")
		return nil, injected
	}
	rv := f.reserver(t)
	r, err := rv.Reserve(t.Context(), baseReserveRequest())
	if !hasIdentity(err, injected) {
		t.Errorf("err = %v, want wrapping %v", err, injected)
	}
	if r.State != ReservationRolledBack {
		t.Errorf("state = %s, want rolled_back", r.State)
	}
	if got := f.rec.snapshot(); fmt.Sprint(got) != "[acquire_leases]" {
		t.Errorf("call order = %v, want [acquire_leases] only (worktree never attempted)", got)
	}
}

// TestReserveFailureAtWorktreeRollsBackLeasesInReverseOrder: leases
// succeed, worktree allocation fails; the leases are released and the
// row reads rolled_back with the compensation recorded.
func TestReserveFailureAtWorktreeRollsBackLeasesInReverseOrder(t *testing.T) {
	f := newReserverFixture(t, 100000)
	injected := errors.New("worktree allocation failed")
	f.seams.AllocateWorktree = func(context.Context, string) (string, error) {
		f.rec.record("allocate_worktree")
		return "", injected
	}
	rv := f.reserver(t)
	r, err := rv.Reserve(t.Context(), baseReserveRequest())
	if !hasIdentity(err, injected) {
		t.Errorf("err = %v, want wrapping %v", err, injected)
	}
	if r.State != ReservationRolledBack || len(r.LeaseIDs) != 0 {
		t.Errorf("row = %+v, want rolled_back with no lease ids", r)
	}
	if got := f.rec.snapshot(); fmt.Sprint(got) != "[acquire_leases allocate_worktree release_leases]" {
		t.Errorf("call order = %v, want leases released and no remove_worktree", got)
	}
	if s := findStep(r, StepLeases); s.State != StepCompensated {
		t.Errorf("leases step = %+v, want compensated", s)
	}
}

// TestReserveRefusesEmptyLeaseSet: a lease seam that returns no id for
// non-empty globs is refused and rolled back, never recorded as held.
func TestReserveRefusesEmptyLeaseSet(t *testing.T) {
	f := newReserverFixture(t, 100000)
	f.seams.AcquireLeases = func(context.Context, string, string, []string) ([]string, error) { return nil, nil }
	r, err := f.reserver(t).Reserve(t.Context(), baseReserveRequest())
	if !cascade.HasKind(err, cascade.KindInternal) || !strings.Contains(err.Error(), "no lease id") || r.State != ReservationRolledBack {
		t.Fatalf("Reserve with an empty lease set = %+v, %v, want rolled_back and KindInternal", r, err)
	}
	if calls := f.rec.snapshot(); len(calls) != 0 {
		t.Fatalf("worktree step ran after an empty lease set: %v", calls)
	}
}

// findStep returns the last ledger entry for name.
func findStep(r Reservation, name StepName) Step {
	for i := len(r.Steps) - 1; i >= 0; i-- {
		if r.Steps[i].Step == name {
			return r.Steps[i]
		}
	}
	return Step{}
}
