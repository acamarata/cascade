package crunch

import (
	"errors"
	"testing"
)

func TestSetAbortNetOnRevert(t *testing.T) {
	stage := map[string]any{
		"net":    "p2",
		"status": "live",
		"count":  3,
	}
	ev := &RevertEvent{Reason: "rollback"}

	got, err := SetAbortNetOnRevert(ev, stage)
	if !errors.Is(err, ErrRevertHalt) {
		t.Fatalf("SetAbortNetOnRevert returned error %v, want %v", err, ErrRevertHalt)
	}

	if got == nil {
		t.Fatal("SetAbortNetOnRevert returned nil stage, want updated stage")
	}
	if got["abortNet"] != true {
		t.Fatalf("got[\"abortNet\"] = %v, want true", got["abortNet"])
	}
	if stage["abortNet"] != true {
		t.Fatalf("stage[\"abortNet\"] = %v, want true; revert must update the original map", stage["abortNet"])
	}

	if got["net"] != "p2" {
		t.Fatalf("got[\"net\"] = %v, want %q", got["net"], "p2")
	}
	if got["status"] != "live" {
		t.Fatalf("got[\"status\"] = %v, want %q", got["status"], "live")
	}
	if got["count"] != 3 {
		t.Fatalf("got[\"count\"] = %v, want %d", got["count"], 3)
	}
	if len(got) != 4 {
		t.Fatalf("len(got) = %d, want 4", len(got))
	}
}

func TestRevertNoOpWithoutEvent(t *testing.T) {
	stage := map[string]any{
		"net": "p2",
	}

	got, err := SetAbortNetOnRevert(nil, stage)
	if err != nil {
		t.Fatalf("SetAbortNetOnRevert(nil, stage) error = %v, want nil", err)
	}
	if got == nil {
		t.Fatal("SetAbortNetOnRevert(nil, stage) returned nil stage, want same stage")
	}
	if _, ok := got["abortNet"]; ok {
		t.Fatalf("got[\"abortNet\"] exists, want absent for nil event")
	}
	if got["net"] != "p2" {
		t.Fatalf("got[\"net\"] = %v, want %q", got["net"], "p2")
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}

	gotNil, errNil := SetAbortNetOnRevert(nil, nil)
	if errNil != nil {
		t.Fatalf("SetAbortNetOnRevert(nil, nil) error = %v, want nil", errNil)
	}
	if gotNil != nil {
		t.Fatalf("SetAbortNetOnRevert(nil, nil) = %v, want nil", gotNil)
	}
}

func TestRevertFailsClosedOnMissingStage(t *testing.T) {
	ev := &RevertEvent{}

	got, err := SetAbortNetOnRevert(ev, nil)
	if got != nil {
		t.Fatalf("SetAbortNetOnRevert(event, nil) = %v, want nil", got)
	}
	if !errors.Is(err, ErrM1454) {
		t.Fatalf("SetAbortNetOnRevert(event, nil) error = %v, want %v", err, ErrM1454)
	}
}
