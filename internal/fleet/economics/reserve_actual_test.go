package economics

import "testing"

// TestReserveReconcileActual: a reported usage is stored verbatim with
// ActualSource=reported; a missing report keeps the estimate with
// ActualSource=estimated, never zero; an unknown source is refused.
func TestReserveReconcileActual(t *testing.T) {
	f := newReserverFixture(t, 100000)
	rv := f.reserver(t)
	r, err := rv.Reserve(t.Context(), baseReserveRequest())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	reported := Actual{TokensIn: 900, TokensOut: 300, Requests: 2}
	got, err := rv.Reconcile(t.Context(), r.ID, reported, ActualSourceReported)
	if err != nil || got.Actual != reported || got.ActualSource != ActualSourceReported {
		t.Fatalf("Reconcile(reported) = %+v, %v", got, err)
	}
	stored, _, _ := f.store.Get(t.Context(), r.ID)
	if stored.Actual != reported || stored.ActualSource != ActualSourceReported || stored.State != ReservationHeld {
		t.Fatalf("stored = %+v, want the reported actual on a still-held row", stored)
	}
	r2, err := rv.Reserve(t.Context(), baseReserveRequest())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	est, err := rv.Reconcile(t.Context(), r2.ID, Actual{}, ActualSourceEstimated)
	want := Actual(r2.Estimate)
	if err != nil || est.Actual != want || est.ActualSource != ActualSourceEstimated || est.Actual == (Actual{}) {
		t.Fatalf("Reconcile(estimated) = %+v, %v, want the estimate %+v kept, never zero", est, err, want)
	}
	if _, err := rv.Reconcile(t.Context(), r2.ID, reported, ActualSource("guessed")); !isKindInvalidInput(err) {
		t.Errorf("Reconcile(unknown source) = %v, want KindInvalidInput", err)
	}
	if _, err := rv.Reconcile(t.Context(), "never-created", reported, ActualSourceReported); err == nil {
		t.Error("Reconcile(missing id) = nil, want not found")
	}
}

// TestReserveScarceUnitsBasePriceOnly: ScarceUnits = BasePrice x actual
// requests; nothing but the stored base price enters it (no pressure,
// mode multiplier or reserve barrier), so two rows with equal base price
// and usage price identically whatever else differs.
func TestReserveScarceUnitsBasePriceOnly(t *testing.T) {
	f := newReserverFixture(t, 100000)
	rv := f.reserver(t)
	a := baseReserveRequest()
	a.BasePrice = 2.5
	b := baseReserveRequest()
	b.BasePrice, b.LaneID, b.ProjectID = 2.5, "lane-executive", "project-crunch"
	ra, errA := rv.Reserve(t.Context(), a)
	rb, errB := rv.Reserve(t.Context(), b)
	if errA != nil || errB != nil {
		t.Fatalf("Reserve: %v %v", errA, errB)
	}
	usage := Actual{TokensIn: 5000, TokensOut: 1000, Requests: 4}
	ga, _ := rv.Reconcile(t.Context(), ra.ID, usage, ActualSourceReported)
	gb, _ := rv.Reconcile(t.Context(), rb.ID, usage, ActualSourceReported)
	if ga.ScarceUnits != 10 || gb.ScarceUnits != ga.ScarceUnits {
		t.Fatalf("ScarceUnits = %v and %v, want 2.5 x 4 = 10 for both", ga.ScarceUnits, gb.ScarceUnits)
	}
	ge, _ := rv.Reconcile(t.Context(), ra.ID, Actual{}, ActualSourceEstimated)
	if ge.ScarceUnits != 2.5*float64(a.Estimate.Requests) {
		t.Fatalf("estimated ScarceUnits = %v, want base price x estimated requests", ge.ScarceUnits)
	}
}
