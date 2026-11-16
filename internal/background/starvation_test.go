package background

import (
	"testing"
	"time"
)

func TestStarvationFirstNoteEmitted(t *testing.T) {
	state := NewStarveState()
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	note, ok := EmitStarvation(state, "A", true, false, now)
	if !ok {
		t.Fatal("expected note to be emitted")
	}
	if note.Root != "A" {
		t.Errorf("expected root A, got %s", note.Root)
	}
	if note.At != now {
		t.Errorf("expected At to be now")
	}
}

func TestStarvationSecondWithin3hSuppressed(t *testing.T) {
	state := NewStarveState()
	t1 := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	EmitStarvation(state, "A", true, false, t1)
	t2 := time.Date(2024, 1, 1, 12, 59, 0, 0, time.UTC)
	_, ok := EmitStarvation(state, "A", true, false, t2)
	if ok {
		t.Fatal("expected note to be suppressed within 3h")
	}
}

func TestStarvationAfter3hEmitsAgain(t *testing.T) {
	state := NewStarveState()
	t1 := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	EmitStarvation(state, "A", true, false, t1)
	t2 := time.Date(2024, 1, 1, 13, 0, 0, 0, time.UTC)
	note, ok := EmitStarvation(state, "A", true, false, t2)
	if !ok {
		t.Fatal("expected note to be emitted after 3h")
	}
	if note.At != t2 {
		t.Errorf("expected At to be t2")
	}
}

func TestStarvationPerRootIndependent(t *testing.T) {
	state := NewStarveState()
	t1 := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	EmitStarvation(state, "A", true, false, t1)
	t2 := time.Date(2024, 1, 1, 10, 30, 0, 0, time.UTC)
	note, ok := EmitStarvation(state, "B", true, false, t2)
	if !ok {
		t.Fatal("expected note for root B")
	}
	if note.Root != "B" {
		t.Errorf("expected root B, got %s", note.Root)
	}
}

func TestStarvationNotIdleOrQueuedNoNote(t *testing.T) {
	state := NewStarveState()
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	_, ok := EmitStarvation(state, "A", false, false, now)
	if ok {
		t.Fatal("expected no note when not idle")
	}
	_, ok = EmitStarvation(state, "A", true, true, now)
	if ok {
		t.Fatal("expected no note when queued")
	}
}
