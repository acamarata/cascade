package economics

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func baseReservation(id string) Reservation {
	return Reservation{
		ID: id, ExecutionID: "exec-" + id, JobID: "job-1", ProjectID: "project-1", LaneID: "lane-1",
		DomainID: "domain-1", ScopeID: "scope-1", RepoID: "repo-1", ScopeGlobs: []string{"src/**"},
		Kind: ReservationInteractive, Estimate: Estimate{TokensIn: 100, TokensOut: 50, Requests: 1},
		ActualSource: ActualSourceEstimated, State: ReservationHeld, OwnerEpoch: "epoch-1", HeartbeatAt: 1000,
	}
}

func TestReservationStoreInsertAndGet(t *testing.T) {
	s := newReservationTestStore(t)
	ctx := t.Context()
	in := baseReservation("r-1")
	in.NodeID, in.SelectedTier, in.Sensitivity, in.DecisionID = "node-1", "tier-1", "internal", "dec-1"
	inserted, err := s.Insert(ctx, in)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if inserted.Created == 0 {
		t.Error("Insert did not stamp Created from the injected clock")
	}
	got, ok, err := s.Get(ctx, "r-1")
	if err != nil || !ok {
		t.Fatalf("Get: got=%+v ok=%v err=%v", got, ok, err)
	}
	if fmt.Sprint(got) != fmt.Sprint(inserted) {
		t.Errorf("Get = %+v, want the inserted row verbatim %+v", got, inserted)
	}
	if _, ok, err := s.Get(ctx, "never-created"); ok || err != nil {
		t.Errorf("Get(missing) ok=%v err=%v, want false, nil", ok, err)
	}
}

func TestReservationStoreInsertRejectsInvalidRows(t *testing.T) {
	s := newReservationTestStore(t)
	ctx := t.Context()
	cases := map[string]func(*Reservation){
		"kind":         func(r *Reservation) { r.Kind = "bogus" },
		"state":        func(r *Reservation) { r.State = "bogus" },
		"id":           func(r *Reservation) { r.ID = "" },
		"execution_id": func(r *Reservation) { r.ExecutionID = "" },
	}
	for name, mutate := range cases {
		r := baseReservation("r-bad-" + name)
		mutate(&r)
		if _, err := s.Insert(ctx, r); !isKindInvalidInput(err) {
			t.Errorf("Insert(bad %s) = %v, want KindInvalidInput", name, err)
		}
	}
}

func TestReservationStoreListByState(t *testing.T) {
	s := newReservationTestStore(t)
	ctx := t.Context()
	if _, err := s.Insert(ctx, baseReservation("r-held")); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	parked := baseReservation("r-parked")
	parked.State = ReservationParked
	if _, err := s.Insert(ctx, parked); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	list, err := s.ListByState(ctx, ReservationHeld)
	if err != nil || len(list) != 1 || list[0].ID != "r-held" {
		t.Errorf("ListByState(held) = %+v, %v, want exactly [r-held]", list, err)
	}
}

func TestReservationStoreTransitionLegalAndIllegal(t *testing.T) {
	s := newReservationTestStore(t)
	ctx := t.Context()
	if _, err := s.Insert(ctx, baseReservation("r-t")); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := s.Transition(ctx, "r-t", ReservationCommitted)
	if err != nil || got.State != ReservationCommitted {
		t.Fatalf("Transition(held->committed) = %+v, %v", got, err)
	}
	_, err = s.Transition(ctx, "r-t", ReservationRolledBack)
	requireSentinel(t, err, ErrReservationTransition, "committed -> rolled_back")
	if _, err := s.Transition(ctx, "never-created", ReservationCommitted); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Errorf("Transition(missing) = %v, want KindNotFound", err)
	}
}

// TestReservationStoreReplaceNeverChangesState: Replace persists step
// and handle progress only; a state change through it is refused, so
// Transition stays the only path that moves State.
func TestReservationStoreReplaceNeverChangesState(t *testing.T) {
	s := newReservationTestStore(t)
	ctx := t.Context()
	r, err := s.Insert(ctx, baseReservation("r-rep"))
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	r.WorktreeID = "wt-9"
	if err := s.Replace(ctx, r); err != nil {
		t.Fatalf("Replace(same state): %v", err)
	}
	r.State = ReservationCommitted
	requireSentinel(t, s.Replace(ctx, r), ErrReservationTransition)
	got, _, _ := s.Get(ctx, "r-rep")
	if got.State != ReservationHeld || got.WorktreeID != "wt-9" {
		t.Errorf("stored = %+v, want held with the replaced worktree id", got)
	}
}

// TestReservationReadableByExecution: GetByExecution returns the row and
// the unique execution_id index refuses a second row with the same id.
func TestReservationReadableByExecution(t *testing.T) {
	s := newReservationTestStore(t)
	ctx := t.Context()
	first, err := s.Insert(ctx, baseReservation("r-exec"))
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, ok, err := s.GetByExecution(ctx, "exec-r-exec")
	if err != nil || !ok || fmt.Sprint(got) != fmt.Sprint(first) {
		t.Fatalf("GetByExecution = %+v ok=%v err=%v, want the inserted row", got, ok, err)
	}
	dup := baseReservation("r-exec-2")
	dup.ExecutionID = "exec-r-exec"
	if _, err := s.Insert(ctx, dup); !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("Insert(duplicate execution id) = %v, want KindConflict from the unique index", err)
	}
	if _, ok, _ := s.Get(ctx, "r-exec-2"); ok {
		t.Error("duplicate execution id row was persisted")
	}
	if _, ok, err := s.GetByExecution(ctx, "exec-missing"); ok || err != nil {
		t.Errorf("GetByExecution(missing) ok=%v err=%v, want false, nil", ok, err)
	}
	if s.DB() == nil {
		t.Error("DB() = nil, want the store's handle")
	}
}

// TestReservationStoreActiveOnScope sums nothing itself: it returns the
// held, parked and committed rows of one scope, excluding one id.
func TestReservationStoreActiveOnScope(t *testing.T) {
	s := newReservationTestStore(t)
	ctx := t.Context()
	rows := map[string]func(*Reservation){
		"r-held":     func(*Reservation) {},
		"r-parked":   func(r *Reservation) { r.State = ReservationParked },
		"r-commit":   func(r *Reservation) { r.State = ReservationCommitted },
		"r-released": func(r *Reservation) { r.State = ReservationReleased },
		"r-other":    func(r *Reservation) { r.ScopeID = "scope-2" },
		"r-self":     func(*Reservation) {},
	}
	for id, mutate := range rows {
		r := baseReservation(id)
		mutate(&r)
		if _, err := s.Insert(ctx, r); err != nil {
			t.Fatalf("Insert(%s): %v", id, err)
		}
	}
	got, err := s.activeOnScope(ctx, "scope-1", "r-self")
	if err != nil {
		t.Fatalf("activeOnScope: %v", err)
	}
	ids := map[string]bool{}
	for _, r := range got {
		ids[r.ID] = true
	}
	if len(ids) != 3 || !ids["r-held"] || !ids["r-parked"] || !ids["r-commit"] {
		t.Errorf("activeOnScope = %v, want exactly r-held, r-parked, r-commit", ids)
	}
}

// concurrentClaim is a claim that lands between a writer's read and its
// write, with the owner_epoch and heartbeat_at it leaves behind.
type concurrentClaim struct {
	name      string
	land      func(ctx context.Context, s *ReservationStore, id string) error
	wantEpoch string
	wantBeat  int64
}

var concurrentClaims = []concurrentClaim{
	{"adopt", func(ctx context.Context, s *ReservationStore, id string) error {
		ok, err := s.adoptEpoch(ctx, id, "epoch-1", "epoch-B", 5000)
		if err == nil && !ok {
			err = errors.New("adoptEpoch matched no row")
		}
		return err
	}, "epoch-B", 5000},
	{"heartbeat", func(ctx context.Context, s *ReservationStore, id string) error {
		_, ok, err := s.touchHeartbeat(ctx, id, 7000)
		if err == nil && !ok {
			err = errors.New("touchHeartbeat matched no row")
		}
		return err
	}, "epoch-1", 7000},
}

// staleProgressWrite is a progress write built from a read taken before
// the claim, with the state it must leave the row in.
type staleProgressWrite struct {
	name  string
	write func(ctx context.Context, s *ReservationStore, r Reservation) error
	want  ReservationState
}

var staleProgressWrites = []staleProgressWrite{
	{"replace", func(ctx context.Context, s *ReservationStore, r Reservation) error {
		r.WorktreeID = "wt-progress"
		return s.Replace(ctx, r)
	}, ReservationHeld},
	{"retire", func(ctx context.Context, s *ReservationStore, r Reservation) error {
		r.WorktreeID = "wt-progress"
		_, err := s.transitionRow(ctx, r, ReservationRolledBack)
		return err
	}, ReservationRolledBack},
}

// TestProgressWritesKeepConcurrentClaim: an Adopt or a Heartbeat that lands
// after a writer read the row and before its progress write must survive
// that write. Replace and the retirement move both write from the stale
// read; neither may roll owner_epoch or heartbeat_at back, yet each lands
// its own progress column.
func TestProgressWritesKeepConcurrentClaim(t *testing.T) {
	for _, c := range concurrentClaims {
		for _, w := range staleProgressWrites {
			t.Run(c.name+"/"+w.name, func(t *testing.T) {
				s := newReservationTestStore(t)
				if _, err := s.Insert(t.Context(), baseReservation("r-1")); err != nil {
					t.Fatalf("Insert: %v", err)
				}
				stale, _, _ := s.Get(t.Context(), "r-1")
				var landErr error
				s.beforeWrite = func(ctx context.Context, id string) {
					s.beforeWrite = nil
					landErr = c.land(ctx, s, id)
				}
				if err := w.write(t.Context(), s, stale); err != nil || landErr != nil {
					t.Fatalf("write = %v, concurrent claim = %v, want both nil", err, landErr)
				}
				got, _, _ := s.Get(t.Context(), "r-1")
				if got.OwnerEpoch != c.wantEpoch || got.HeartbeatAt != c.wantBeat {
					t.Errorf("owner_epoch=%q heartbeat_at=%d, want %q and %d (the claim that landed after the read)", got.OwnerEpoch, got.HeartbeatAt, c.wantEpoch, c.wantBeat)
				}
				if got.State != w.want || got.WorktreeID != "wt-progress" {
					t.Errorf("state=%s worktree=%q, want %s and the progress write", got.State, got.WorktreeID, w.want)
				}
			})
		}
	}
}
