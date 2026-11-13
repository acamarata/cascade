package background

import (
	"errors"
	"testing"
)

type mockBuilder struct {
	failBrief string
}

func (m *mockBuilder) Build(brief string) error {
	if brief == m.failBrief {
		return errors.New("build failed")
	}
	return nil
}

// TestUnbuildableTaskRemainsQueued verifies that a task whose brief cannot be
// built remains queued and does not consume any start or account slot.
func TestUnbuildableTaskRemainsQueued(t *testing.T) {
	b := &mockBuilder{failBrief: "bad-brief"}
	q := BriefQueue{
		Tasks: []BriefTask{
			{ID: "t1", Brief: "good-brief", Status: "queued"},
			{ID: "t2", Brief: "bad-brief", Status: "queued"},
		},
		StartSlotsUsed:   0,
		AccountSlotsUsed: 0,
	}

	err := validateBriefQueue(q, "q1", b)
	if err != nil {
		t.Fatalf("validateBriefQueue returned error: %v", err)
	}

	if q.StartSlotsUsed != 0 {
		t.Errorf("expected StartSlotsUsed 0, got %d", q.StartSlotsUsed)
	}
	if q.AccountSlotsUsed != 0 {
		t.Errorf("expected AccountSlotsUsed 0, got %d", q.AccountSlotsUsed)
	}

	for _, task := range q.Tasks {
		if task.Brief == "bad-brief" && task.Status != "queued" {
			t.Errorf("unbuildable task must remain queued, got status %q", task.Status)
		}
	}
}

func TestValidateBriefQueueBlankID(t *testing.T) {
	b := &mockBuilder{}
	err := validateBriefQueue(BriefQueue{}, "   ", b)
	if !errors.Is(err, ErrInvalidBriefQueue) {
		t.Errorf("expected ErrInvalidBriefQueue, got %v", err)
	}
}

func TestValidateBriefQueueNilBuilder(t *testing.T) {
	err := validateBriefQueue(BriefQueue{}, "id", nil)
	if !errors.Is(err, ErrInvalidBriefQueue) {
		t.Errorf("expected ErrInvalidBriefQueue, got %v", err)
	}
}

func TestValidateBriefQueueNegativeCounters(t *testing.T) {
	b := &mockBuilder{}
	q := BriefQueue{StartSlotsUsed: -1}
	err := validateBriefQueue(q, "id", b)
	if !errors.Is(err, ErrInvalidBriefQueue) {
		t.Errorf("expected ErrInvalidBriefQueue, got %v", err)
	}
}

func TestValidateBriefQueueDuplicateTaskID(t *testing.T) {
	b := &mockBuilder{}
	q := BriefQueue{
		Tasks: []BriefTask{
			{ID: "t1", Brief: "b1", Status: "queued"},
			{ID: "t1", Brief: "b2", Status: "running"},
		},
	}
	err := validateBriefQueue(q, "id", b)
	if !errors.Is(err, ErrInvalidBriefQueue) {
		t.Errorf("expected ErrInvalidBriefQueue, got %v", err)
	}
}

func TestValidateBriefQueueBlankTaskID(t *testing.T) {
	b := &mockBuilder{}
	q := BriefQueue{
		Tasks: []BriefTask{
			{ID: "  ", Brief: "b1", Status: "queued"},
		},
	}
	err := validateBriefQueue(q, "id", b)
	if !errors.Is(err, ErrInvalidBriefQueue) {
		t.Errorf("expected ErrInvalidBriefQueue, got %v", err)
	}
}

func TestValidateBriefQueueBlankBrief(t *testing.T) {
	b := &mockBuilder{}
	q := BriefQueue{
		Tasks: []BriefTask{
			{ID: "t1", Brief: "", Status: "queued"},
		},
	}
	err := validateBriefQueue(q, "id", b)
	if !errors.Is(err, ErrInvalidBriefQueue) {
		t.Errorf("expected ErrInvalidBriefQueue, got %v", err)
	}
}

func TestValidateBriefQueueInvalidStatus(t *testing.T) {
	b := &mockBuilder{}
	q := BriefQueue{
		Tasks: []BriefTask{
			{ID: "t1", Brief: "b1", Status: "done"},
		},
	}
	err := validateBriefQueue(q, "id", b)
	if !errors.Is(err, ErrInvalidBriefQueue) {
		t.Errorf("expected ErrInvalidBriefQueue, got %v", err)
	}
}

func TestValidateBriefQueueEmptyTasks(t *testing.T) {
	b := &mockBuilder{}
	err := validateBriefQueue(BriefQueue{}, "id", b)
	if err != nil {
		t.Errorf("expected nil error for empty tasks, got %v", err)
	}
}
