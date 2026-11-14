package nesting

import (
	"testing"
)

type fakeLane struct {
	capacity int
	acquired int
}

func (f *fakeLane) TryAcquire() bool {
	if f == nil {
		return false
	}
	if f.acquired >= f.capacity {
		return false
	}
	f.acquired++
	return true
}

func (f *fakeLane) Release() {
	if f == nil {
		return
	}
	if f.acquired > 0 {
		f.acquired--
	}
}

func TestDepth10Admitted(t *testing.T) {
	lane := &fakeLane{capacity: 1}
	l := NewLimiter(lane)

	task1 := Task{ID: "t1", Depth: 10}
	state, err := l.Admit(task1)
	if err != nil {
		t.Fatalf("Admit returned error: %v", err)
	}
	if state != "running" {
		t.Fatalf("expected state running, got %q", state)
	}
	if l.Running() != 1 {
		t.Fatalf("expected Running() == 1, got %d", l.Running())
	}

	task2 := Task{ID: "t2", Depth: 10}
	state2, err := l.Admit(task2)
	if err != nil {
		t.Fatalf("Admit returned error: %v", err)
	}
	if state2 != "queued" {
		t.Fatalf("expected state queued, got %q", state2)
	}
	if l.Running() != 1 {
		t.Fatalf("expected Running() == 1, got %d", l.Running())
	}
	q := l.Queued()
	if len(q) != 1 || q[0].ID != "t2" {
		t.Fatalf("expected queued to contain t2, got %v", q)
	}
}
