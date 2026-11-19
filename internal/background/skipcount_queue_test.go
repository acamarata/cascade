package background

import (
	"errors"
	"testing"
)

type queueTestPanicLive struct{}

func (queueTestPanicLive) LiveCount() (int, error) {
	panic("LiveCount should not be called")
}

type queueTestCountingEnqueuer struct {
	calls []string
	err   error
}

func (c *queueTestCountingEnqueuer) Enqueue(id string) error {
	c.calls = append(c.calls, id)
	return c.err
}

func TestQueueAddBypassesLiveCount(t *testing.T) {
	var live LiveCountSource = queueTestPanicLive{}
	q := &queueTestCountingEnqueuer{}
	req := ScheduleRequest{
		ID:      "item-42",
		Trigger: TriggerQueueAdd,
		MaxLive: 999,
	}

	ok, err := Schedule(req, live, q)
	if err != nil {
		t.Fatalf("Schedule returned error: %v", err)
	}
	if !ok {
		t.Fatalf("Schedule returned false, want true")
	}
	if len(q.calls) != 1 {
		t.Fatalf("Enqueue called %d times, want 1", len(q.calls))
	}
	if q.calls[0] != "item-42" {
		t.Fatalf("Enqueue called with %q, want %q", q.calls[0], "item-42")
	}

	q2 := &queueTestCountingEnqueuer{}
	req2 := ScheduleRequest{
		ID:      "item-43",
		Trigger: TriggerQueueAdd,
	}
	ok2, err2 := Schedule(req2, nil, q2)
	if err2 != nil {
		t.Fatalf("Schedule with nil live returned error: %v", err2)
	}
	if !ok2 {
		t.Fatalf("Schedule with nil live returned false, want true")
	}
	if len(q2.calls) != 1 {
		t.Fatalf("Enqueue called %d times for nil live, want 1", len(q2.calls))
	}

	q3 := &queueTestCountingEnqueuer{}
	req3 := ScheduleRequest{ID: "x", Trigger: Trigger(99)}
	ok3, err3 := Schedule(req3, nil, q3)
	if !errors.Is(err3, ErrInvalidSchedule) {
		t.Fatalf("Expected ErrInvalidSchedule for invalid trigger, got %v", err3)
	}
	if ok3 {
		t.Fatalf("Expected false for invalid trigger")
	}

	req4 := ScheduleRequest{ID: "x", Trigger: TriggerQueueAdd}
	ok4, err4 := Schedule(req4, nil, nil)
	if !errors.Is(err4, ErrInvalidSchedule) {
		t.Fatalf("Expected ErrInvalidSchedule for nil queue, got %v", err4)
	}
	if ok4 {
		t.Fatalf("Expected false for nil queue")
	}
}
