package economics

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/governor"
)

func TestReserveWithPermitHoldsOnlyForTheCall(t *testing.T) {
	f := newReserverFixture(t, 100000)
	rv := f.reserver(t)
	r, err := rv.Reserve(t.Context(), baseReserveRequest())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	f.rec.logs = nil // discard Reserve's own acquisition log

	called := false
	err = rv.WithPermit(t.Context(), r.ID, func(_ context.Context) error {
		called = true
		got := f.rec.snapshot()
		if len(got) != 1 || got[0] != "permit" {
			t.Fatalf("permit not held during fn: log = %v", got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithPermit: %v", err)
	}
	if !called {
		t.Fatal("fn was never called")
	}
}

func TestReserveWithPermitReleasesOnFnError(t *testing.T) {
	f := newReserverFixture(t, 100000)
	rv := f.reserver(t)
	r, err := rv.Reserve(t.Context(), baseReserveRequest())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	sentinel := errors.New("adapter call failed")
	err = rv.WithPermit(t.Context(), r.ID, func(_ context.Context) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WithPermit error = %v, want %v", err, sentinel)
	}
}

func TestReserveParkedHoldsNoPermit(t *testing.T) {
	f := newReserverFixture(t, 100000)
	rv := f.reserver(t)
	r, err := rv.Reserve(t.Context(), baseReserveRequest())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if _, err := rv.Park(t.Context(), r.ID); err != nil {
		t.Fatalf("Park: %v", err)
	}
	f.rec.logs = nil

	err = rv.WithPermit(t.Context(), r.ID, func(_ context.Context) error {
		t.Fatal("fn must not run for a parked reservation")
		return nil
	})
	if !errors.Is(err, ErrReservationTransition) {
		t.Fatalf("WithPermit(parked) error = %v, want ErrReservationTransition", err)
	}
	if got := f.rec.snapshot(); len(got) != 0 {
		t.Fatalf("permit acquired for a parked reservation: log = %v", got)
	}

	if _, err := rv.Unpark(t.Context(), r.ID); err != nil {
		t.Fatalf("Unpark: %v", err)
	}
	if err := rv.WithPermit(t.Context(), r.ID, func(_ context.Context) error { return nil }); err != nil {
		t.Fatalf("WithPermit after Unpark: %v", err)
	}
}

func TestReserveWithPermitUnknownReservation(t *testing.T) {
	f := newReserverFixture(t, 100000)
	rv := f.reserver(t)
	err := rv.WithPermit(t.Context(), "does-not-exist", func(_ context.Context) error { return nil })
	if err == nil {
		t.Fatal("WithPermit(unknown id) = nil error, want error")
	}
}

func TestReserveWithPermitRejectsEmptyArgs(t *testing.T) {
	f := newReserverFixture(t, 100000)
	rv := f.reserver(t)
	if err := rv.WithPermit(t.Context(), "", func(_ context.Context) error { return nil }); err == nil {
		t.Error("WithPermit(empty id) = nil error, want error")
	}
	if err := rv.WithPermit(t.Context(), "some-id", nil); err == nil {
		t.Error("WithPermit(nil fn) = nil error, want error")
	}
}

func TestReserveWithPermitPropagatesPermitError(t *testing.T) {
	f := newReserverFixture(t, 100000)
	rv := f.reserver(t)
	r, err := rv.Reserve(t.Context(), baseReserveRequest())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	sentinel := errors.New("admission refused")
	rv.permit = func(_ context.Context, _ governor.AdmissionRequest) (governor.Permit, error) {
		return governor.Permit{}, sentinel
	}
	err = rv.WithPermit(t.Context(), r.ID, func(_ context.Context) error {
		t.Fatal("fn must not run when permit acquisition fails")
		return nil
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WithPermit error = %v, want %v", err, sentinel)
	}
}
