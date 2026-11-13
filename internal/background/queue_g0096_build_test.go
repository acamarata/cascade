package background

import (
	"errors"
	"math"
	"testing"
)

// errorBuilder is a test mock that always returns the configured error.
type errorBuilder struct {
	err       error
	lastBrief string
	callCount int
}

func (e *errorBuilder) Build(brief string) error {
	e.callCount++
	e.lastBrief = brief
	return e.err
}

type unbuildableCase struct {
	name         string
	brief        string
	buildErr     error
	expectedNote string
}

func unbuildableCases() []unbuildableCase {
	return []unbuildableCase{
		{
			name:         "simple error message",
			brief:        "test brief content",
			buildErr:     errors.New("build failed: missing dependency"),
			expectedNote: "build failed: missing dependency",
		},
		{
			name:         "wrapped error message",
			brief:        "another brief",
			buildErr:     errors.New("outer: " + errors.New("inner error").Error()),
			expectedNote: "outer: inner error",
		},
		{
			name:         "empty error message",
			brief:        "brief with empty error",
			buildErr:     errors.New(""),
			expectedNote: "",
		},
	}
}

func runUnbuildableCase(t *testing.T, tt unbuildableCase) {
	t.Helper()
	builder := &errorBuilder{
		err: tt.buildErr,
	}

	q := BriefQueue{
		Tasks: []BriefTask{
			{
				ID:     "task-123",
				Brief:  tt.brief,
				Status: "queued",
			},
		},
		StartSlotsUsed:   2,
		AccountSlotsUsed: 3,
	}

	result, err := BuildQueuedBrief(q, "task-123", builder)

	if err != nil {
		t.Fatalf("BuildQueuedBrief returned error: %v, want nil", err)
	}

	if builder.callCount != 1 {
		t.Fatalf("builder called %d times, want 1", builder.callCount)
	}

	if builder.lastBrief != tt.brief {
		t.Fatalf("builder received brief %q, want %q", builder.lastBrief, tt.brief)
	}

	checkUnbuiltResult(t, q, result, tt.expectedNote)
}

func checkUnbuiltResult(t *testing.T, q, result BriefQueue, expectedNote string) {
	t.Helper()
	if len(result.Tasks) != 1 {
		t.Fatalf("result has %d tasks, want 1", len(result.Tasks))
	}

	if result.Tasks[0].Note != expectedNote {
		t.Errorf("task Note = %q, want %q", result.Tasks[0].Note, expectedNote)
	}

	if result.Tasks[0].Status != "queued" {
		t.Errorf("task Status = %q, want %q", result.Tasks[0].Status, "queued")
	}

	if result.StartSlotsUsed != 2 {
		t.Errorf("StartSlotsUsed = %d, want 2", result.StartSlotsUsed)
	}

	if result.AccountSlotsUsed != 3 {
		t.Errorf("AccountSlotsUsed = %d, want 3", result.AccountSlotsUsed)
	}

	if q.Tasks[0].Note != "" {
		t.Errorf("original queue task Note was mutated to %q, want empty", q.Tasks[0].Note)
	}
}

// TestUnbuildableTaskStoresReasonAsNote verifies that when a builder returns an error,
// the error message is copied verbatim into the task's Note field, and the original
// Brief is passed to the builder exactly once unchanged.
func TestUnbuildableTaskStoresReasonAsNote(t *testing.T) {
	for _, tt := range unbuildableCases() {
		t.Run(tt.name, func(t *testing.T) {
			runUnbuildableCase(t, tt)
		})
	}
}

// TestUnbuildableTaskOverflow verifies that counter overflow returns ErrInvalidBriefQueue.
func TestUnbuildableTaskOverflow(t *testing.T) {
	builder := &errorBuilder{err: errors.New("should not be called")}

	q := BriefQueue{
		Tasks: []BriefTask{
			{
				ID:     "task-123",
				Brief:  "test brief",
				Status: "queued",
			},
		},
		StartSlotsUsed:   math.MaxInt,
		AccountSlotsUsed: 0,
	}

	_, err := BuildQueuedBrief(q, "task-123", builder)

	if err != ErrInvalidBriefQueue {
		t.Errorf("BuildQueuedBrief returned error %v, want %v", err, ErrInvalidBriefQueue)
	}

	if builder.callCount != 0 {
		t.Errorf("builder called %d times, want 0", builder.callCount)
	}
}
