package crunch

import (
	"errors"
	"testing"
)

func hookTestTasks() []HookTask {
	return []HookTask{
		{ID: "a", Priority: 1},
		{ID: "b", Priority: 3},
		{ID: "c", Priority: 2},
	}
}

func assertOrder(t *testing.T, out []HookTask, want, msg string) {
	t.Helper()
	if len(out) != len(want) {
		t.Fatalf("%s: %+v", msg, out)
	}
	for i := range want {
		if out[i].ID != want[i:i+1] {
			t.Fatalf("%s: %+v", msg, out)
		}
	}
}

func TestPriorityHookOverridesOrder(t *testing.T) {
	tasks := hookTestTasks()

	// nil hook stably sorts descending
	out, err := Schedule(tasks, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertOrder(t, out, "bca", "nil hook did not sort descending")

	tasksEqual := []HookTask{
		{ID: "a", Priority: 2},
		{ID: "b", Priority: 2},
		{ID: "c", Priority: 1},
	}
	outEq, err := Schedule(tasksEqual, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertOrder(t, outEq, "abc", "nil hook did not preserve order for equal priorities")

	// hook returns high before low
	hook := func(ts []HookTask) ([]HookTask, error) {
		return []HookTask{ts[1], ts[2], ts[0]}, nil // b, c, a
	}
	out2, err := Schedule(tasks, hook)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertOrder(t, out2, "bca", "hook did not override order")
}

func TestScheduleRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name  string
		tasks []HookTask
	}{
		{"empty", []HookTask{}},
		{"duplicate", []HookTask{{ID: "a", Priority: 1}, {ID: "a", Priority: 2}}},
		{"empty ID", []HookTask{{ID: "", Priority: 1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Schedule(tc.tasks, nil)
			if !errors.Is(err, ErrInvalidHook) {
				t.Fatalf("expected ErrInvalidHook for %s, got %v", tc.name, err)
			}
		})
	}
}

func TestScheduleRejectsBadHookResults(t *testing.T) {
	cases := []struct {
		name string
		hook PriorityHook
	}{
		{"altered task", func(ts []HookTask) ([]HookTask, error) {
			ts[0].Priority = 99
			return ts, nil
		}},
		{"missing task", func(ts []HookTask) ([]HookTask, error) {
			return ts[:1], nil
		}},
		{"added task", func(ts []HookTask) ([]HookTask, error) {
			return append(ts, HookTask{ID: "z", Priority: 1}), nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Schedule(hookTestTasks(), tc.hook)
			if !errors.Is(err, ErrInvalidHook) {
				t.Fatalf("expected ErrInvalidHook for %s, got %v", tc.name, err)
			}
		})
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
