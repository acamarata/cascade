package doctor

import (
	"context"
	"testing"
)

func TestDoctorSelfCheck(t *testing.T) {
	c := NewDoctorSelfCheck()

	if c.Name() != "doctor_selfcheck" {
		t.Fatalf("got Name()=%q", c.Name())
	}
	if !c.Metadata().FirstRun || !c.Metadata().Fixable {
		t.Fatalf("got Metadata()=%+v, want FirstRun+Fixable both true", c.Metadata())
	}

	res, err := c.Run(context.Background())
	if err != nil || res.Status != StatusOK {
		t.Fatalf("got %+v, err=%v, want StatusOK", res, err)
	}

	fx, err := c.Fix(context.Background())
	if err != nil || fx.Applied || fx.Delta != "" {
		t.Fatalf("got %+v, err=%v, want zero-value already-correct FixResult", fx, err)
	}
}

func TestDoctorSelfCheck_DoneContextIsUnverifiable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := NewDoctorSelfCheck()
	res, err := c.Run(ctx)
	if err != nil {
		t.Fatalf("Run returned err=%v, want nil (status carries the failure)", err)
	}
	if res.Status != StatusError {
		t.Fatalf("got status=%v, want StatusError for an already-done context (Art.1: unverifiable subject is never OK)", res.Status)
	}
}

// TestDoctorSelfCheck_EndToEndViaRegistry proves the registry can
// register and run this one check end-to-end (the ticket contract's
// stated purpose for DoctorSelfCheck).
func TestDoctorSelfCheck_EndToEndViaRegistry(t *testing.T) {
	reg := NewCheckRegistry()
	reg.Register(NewDoctorSelfCheck())

	report := Run(context.Background(), reg.List(), RunOptions{Clock: fixedTestClock()})
	if len(report.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(report.Entries))
	}
	if report.Outcome() != OutcomeOK {
		t.Fatalf("got outcome=%v, want OutcomeOK", report.Outcome())
	}
}

func TestDoctorSelfCheck_Describe(t *testing.T) {
	c := NewDoctorSelfCheck()
	if c.Describe() == "" {
		t.Fatalf("Describe() must not be empty")
	}
}
