package background

import (
	"errors"
	"testing"
)

type panicLive struct{}

func (panicLive) LiveCount() (int, error) {
	panic("LiveCount must not be called for manual trigger")
}

type countingEnqueuer struct {
	calls  int
	lastID string
	err    error
}

func (c *countingEnqueuer) Enqueue(id string) error {
	c.calls++
	c.lastID = id
	return c.err
}

// TestManualStartBypassesLiveCount verifies TriggerManual ignores MaxLive and never calls LiveCount.
func TestManualStartBypassesLiveCount(t *testing.T) {
	eq := &countingEnqueuer{}
	req := ScheduleRequest{ID: "item-1", Trigger: TriggerManual, MaxLive: 0}
	ok, err := Schedule(req, panicLive{}, eq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected true")
	}
	if eq.calls != 1 {
		t.Fatalf("expected 1 enqueue call, got %d", eq.calls)
	}
	if eq.lastID != "item-1" {
		t.Fatalf("expected ID item-1, got %s", eq.lastID)
	}

	req2 := ScheduleRequest{ID: "item-2", Trigger: TriggerManual, MaxLive: -5}
	ok, err = Schedule(req2, nil, eq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected true for negative maxlive")
	}
}

var errSentinel = errors.New("sentinel queue error")

// TestManualStartEnqueueSuccess verifies valid manual scheduling returns true,nil after one Enqueue.
func TestManualStartEnqueueSuccess(t *testing.T) {
	eq := &countingEnqueuer{}
	req := ScheduleRequest{ID: "valid-id", Trigger: TriggerManual, MaxLive: 10}
	ok, err := Schedule(req, nil, eq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected true")
	}
	if eq.calls != 1 {
		t.Fatalf("expected 1 call, got %d", eq.calls)
	}

	reqBlank := ScheduleRequest{ID: "   ", Trigger: TriggerManual}
	ok, err = Schedule(reqBlank, nil, eq)
	if !errors.Is(err, ErrInvalidSchedule) {
		t.Fatalf("expected ErrInvalidSchedule, got %v", err)
	}
	if ok {
		t.Fatal("expected false for blank ID")
	}
	if eq.calls != 1 {
		t.Fatalf("enqueue should not have been called again, got %d", eq.calls)
	}

	eqErr := &countingEnqueuer{err: errSentinel}
	reqErr := ScheduleRequest{ID: "err-id", Trigger: TriggerManual}
	ok, err = Schedule(reqErr, nil, eqErr)
	if !errors.Is(err, errSentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	if ok {
		t.Fatal("expected false on enqueue error")
	}
	if eqErr.calls != 1 {
		t.Fatalf("expected 1 call, got %d", eqErr.calls)
	}
}
