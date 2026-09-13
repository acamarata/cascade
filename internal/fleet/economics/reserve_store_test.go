package economics

import "testing"

func baseReservation(id string) Reservation {
	return Reservation{
		ID: id, JobID: "job-1", ProjectID: "project-1", LaneID: "lane-1",
		DomainID: "domain-1", ScopeID: "scope-1", Kind: ReservationInteractive,
		Estimate:     Estimate{TokensIn: 100, TokensOut: 50, Requests: 1},
		ActualSource: ActualSourceEstimated, State: ReservationHeld,
		OwnerEpoch: "epoch-1", HeartbeatAt: 1000,
	}
}

func TestReservationStoreInsertAndGet(t *testing.T) {
	s := newReservationTestStore(t)
	ctx := t.Context()
	inserted, err := s.Insert(ctx, baseReservation("r-1"))
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
	if got.State != ReservationHeld || got.DomainID != "domain-1" {
		t.Errorf("Get = %+v, want held/domain-1", got)
	}
}

func TestReservationStoreGetNotFoundNoError(t *testing.T) {
	s := newReservationTestStore(t)
	_, ok, err := s.Get(t.Context(), "never-created")
	if err != nil {
		t.Fatalf("Get: err=%v, want nil", err)
	}
	if ok {
		t.Error("Get: ok=true, want false")
	}
}

func TestReservationStoreInsertRejectsUnknownKindAndState(t *testing.T) {
	s := newReservationTestStore(t)
	ctx := t.Context()
	bad := baseReservation("r-bad-kind")
	bad.Kind = ReservationKind("bogus")
	if _, err := s.Insert(ctx, bad); err == nil {
		t.Error("Insert(unknown kind) = nil error, want error")
	}
	bad2 := baseReservation("r-bad-state")
	bad2.State = ReservationState("bogus")
	if _, err := s.Insert(ctx, bad2); err == nil {
		t.Error("Insert(unknown state) = nil error, want error")
	}
}

func TestReservationStoreListByState(t *testing.T) {
	s := newReservationTestStore(t)
	ctx := t.Context()
	held := baseReservation("r-held")
	if _, err := s.Insert(ctx, held); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	parked := baseReservation("r-parked")
	parked.State = ReservationParked
	if _, err := s.Insert(ctx, parked); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	list, err := s.ListByState(ctx, ReservationHeld)
	if err != nil {
		t.Fatalf("ListByState: %v", err)
	}
	if len(list) != 1 || list[0].ID != "r-held" {
		t.Errorf("ListByState(held) = %+v, want exactly [r-held]", list)
	}
}

func TestReservationStoreTransitionLegalAndIllegal(t *testing.T) {
	s := newReservationTestStore(t)
	ctx := t.Context()
	if _, err := s.Insert(ctx, baseReservation("r-t")); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := s.Transition(ctx, "r-t", ReservationCommitted)
	if err != nil {
		t.Fatalf("Transition(held->committed): %v", err)
	}
	if got.State != ReservationCommitted {
		t.Errorf("state = %s, want committed", got.State)
	}
	// committed -> rolled_back is not in the legal table.
	if _, err := s.Transition(ctx, "r-t", ReservationRolledBack); err == nil {
		t.Error("Transition(committed->rolled_back) = nil error, want ErrReservationTransition")
	}
}

func TestReservationStoreTransitionNotFound(t *testing.T) {
	s := newReservationTestStore(t)
	if _, err := s.Transition(t.Context(), "never-created", ReservationCommitted); err == nil {
		t.Error("Transition(missing id) = nil error, want not-found error")
	}
}

func TestReservationStoreOutstandingHeldSumsHeldParkedCommittedOnly(t *testing.T) {
	s := newReservationTestStore(t)
	ctx := t.Context()
	held := baseReservation("r-a")
	held.Estimate = Estimate{TokensIn: 10, TokensOut: 10, Requests: 1} // weight 21
	if _, err := s.Insert(ctx, held); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	parked := baseReservation("r-b")
	parked.State = ReservationParked
	parked.Estimate = Estimate{TokensIn: 5, TokensOut: 5, Requests: 1} // weight 11
	if _, err := s.Insert(ctx, parked); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	released := baseReservation("r-c")
	released.State = ReservationReleased
	released.Estimate = Estimate{TokensIn: 999, TokensOut: 999, Requests: 999} // must NOT count
	if _, err := s.Insert(ctx, released); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	total, err := s.OutstandingHeld(ctx, "domain-1")
	if err != nil {
		t.Fatalf("OutstandingHeld: %v", err)
	}
	if total != 32 { // 21 + 11, released excluded
		t.Errorf("OutstandingHeld = %d, want 32 (released rows excluded)", total)
	}
}

// TestReservationStoreRoundTripsActualSource proves the store persists
// and returns BOTH ActualSource values verbatim (Insert/Get is not
// hardcoded to the estimated default).
func TestReservationStoreRoundTripsActualSource(t *testing.T) {
	s := newReservationTestStore(t)
	ctx := t.Context()
	reported := baseReservation("r-reported")
	reported.ActualSource = ActualSourceReported
	reported.Actual = Actual{TokensIn: 42, TokensOut: 7, Requests: 1}
	if _, err := s.Insert(ctx, reported); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, ok, err := s.Get(ctx, "r-reported")
	if err != nil || !ok {
		t.Fatalf("Get: %v %v", ok, err)
	}
	if got.ActualSource != ActualSourceReported || got.Actual.TokensIn != 42 {
		t.Errorf("Get = %+v, want ActualSource=reported with Actual round-tripped", got)
	}
}

func TestReservationStoreOutstandingHeldExceptExcludesSelf(t *testing.T) {
	s := newReservationTestStore(t)
	ctx := t.Context()
	self := baseReservation("r-self")
	self.Estimate = Estimate{TokensIn: 50, TokensOut: 0, Requests: 0}
	if _, err := s.Insert(ctx, self); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	other := baseReservation("r-other")
	other.Estimate = Estimate{TokensIn: 7, TokensOut: 0, Requests: 0}
	if _, err := s.Insert(ctx, other); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	total, err := s.outstandingHeldExcept(ctx, "domain-1", "r-self")
	if err != nil {
		t.Fatalf("outstandingHeldExcept: %v", err)
	}
	if total != 7 {
		t.Errorf("outstandingHeldExcept(exclude r-self) = %d, want 7 (only r-other)", total)
	}
}
