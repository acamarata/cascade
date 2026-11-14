package background

import (
	"errors"
	"testing"
)

type failLiveSource struct{}

func (failLiveSource) LiveCount() (int, error) {
	return 0, errors.New("live count must not be called")
}

type recordEnqueuer struct {
	ids []string
	err error
}

func (r *recordEnqueuer) Enqueue(id string) error {
	r.ids = append(r.ids, id)
	return r.err
}

// TestQueueAddBypassesLiveCount verifies TriggerQueueAdd bypasses live count.
func TestQueueAddBypassesLiveCount(t *testing.T) {
	q := &recordEnqueuer{}
	req := ScheduleRequest{
		ID:      "item-1",
		Trigger: TriggerQueueAdd,
		MaxLive: 99,
	}

	ok, err := Schedule(req, failLiveSource{}, q)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected true, got false")
	}
	if len(q.ids) != 1 {
		t.Fatalf("expected 1 enqueue call, got %d", len(q.ids))
	}
	if q.ids[0] != "item-1" {
		t.Fatalf("expected id item-1, got %s", q.ids[0])
	}

	// Verify nil live source is also accepted for queue-add.
	q2 := &recordEnqueuer{}
	ok2, err2 := Schedule(req, nil, q2)
	if err2 != nil {
		t.Fatalf("unexpected error with nil live: %v", err2)
	}
	if !ok2 {
		t.Fatal("expected true with nil live, got false")
	}
	if len(q2.ids) != 1 {
		t.Fatalf("expected 1 enqueue call with nil live, got %d", len(q2.ids))
	}
}
