package crunch

import (
	"errors"
	"testing"
)

func TestPriorityHookOverridesOrder(t *testing.T) {
	tasks := []HookTask{
		{ID: "a", Priority: 1},
		{ID: "b", Priority: 3},
		{ID: "c", Priority: 2},
	}

	// nil hook stably sorts descending
	out, err := Schedule(tasks, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 3 || out[0].ID != "b" || out[1].ID != "c" || out[2].ID != "a" {
		t.Fatalf("nil hook did not sort descending: %+v", out)
	}

	tasksEqual := []HookTask{
		{ID: "a", Priority: 2},
		{ID: "b", Priority: 2},
		{ID: "c", Priority: 1},
	}
	outEq, err := Schedule(tasksEqual, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(outEq) != 3 || outEq[0].ID != "a" || outEq[1].ID != "b" || outEq[2].ID != "c" {
		t.Fatalf("nil hook did not preserve order for equal priorities: %+v", outEq)
	}

	// hook returns high before low
	hook := func(ts []HookTask) ([]HookTask, error) {
		return []HookTask{ts[1], ts[2], ts[0]}, nil // b, c, a
	}
	out2, err := Schedule(tasks, hook)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out2) != 3 || out2[0].ID != "b" || out2[1].ID != "c" || out2[2].ID != "a" {
		t.Fatalf("hook did not override order: %+v", out2)
	}

	// invalid, duplicate, empty or altered tasks return ErrInvalidHook
	_, err = Schedule([]HookTask{}, nil)
	if !errors.Is(err, ErrInvalidHook) {
		t.Fatalf("expected ErrInvalidHook for empty, got %v", err)
	}

	_, err = Schedule([]HookTask{{ID: "a", Priority: 1}, {ID: "a", Priority: 2}}, nil)
	if !errors.Is(err, ErrInvalidHook) {
		t.Fatalf("expected ErrInvalidHook for duplicate, got %v", err)
	}

	_, err = Schedule([]HookTask{{ID: "", Priority: 1}}, nil)
	if !errors.Is(err, ErrInvalidHook) {
		t.Fatalf("expected ErrInvalidHook for empty ID, got %v", err)
	}

	// altered tasks
	alteredHook := func(ts []HookTask) ([]HookTask, error) {
		ts[0].Priority = 99
		return ts, nil
	}
	_, err = Schedule(tasks, alteredHook)
	if !errors.Is(err, ErrInvalidHook) {
		t.Fatalf("expected ErrInvalidHook for altered task, got %v", err)
	}

	// missing task
	missingHook := func(ts []HookTask) ([]HookTask, error) {
		return ts[:1], nil
	}
	_, err = Schedule(tasks, missingHook)
	if !errors.Is(err, ErrInvalidHook) {
		t.Fatalf("expected ErrInvalidHook for missing task, got %v", err)
	}

	// added task
	addedHook := func(ts []HookTask) ([]HookTask, error) {
		return append(ts, HookTask{ID: "z", Priority: 1}), nil
	}
	_, err = Schedule(tasks, addedHook)
	if !errors.Is(err, ErrInvalidHook) {
		t.Fatalf("expected ErrInvalidHook for added task, got %v", err)
	}
}

func TestPriorityHookEmptyList(t *testing.T) {
	called := false
	hook := func(ts []HookTask) ([]HookTask, error) {
		called = true
		return ts, nil
	}
	out, err := Schedule([]HookTask{}, hook)
	if !errors.Is(err, ErrInvalidHook) {
		t.Fatalf("expected ErrInvalidHook for empty list, got %v", err)
	}
	if out != nil {
		t.Fatalf("expected nil output for empty list, got %+v", out)
	}
	if called {
		t.Fatalf("hook should not be called for empty list")
	}
}
