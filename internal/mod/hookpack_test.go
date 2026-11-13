package mod

import (
	"errors"
	"strings"
	"testing"
)

func TestRunAllExecutesInOrder(t *testing.T) {
	hp := NewHookPack()
	var order []string
	hp.Register("a", func() error { order = append(order, "a"); return nil })
	hp.Register("b", func() error { order = append(order, "b"); return nil })
	if err := hp.RunAll(); err != nil {
		t.Fatalf("RunAll returned error: %v", err)
	}
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Fatalf("expected order [a b], got %v", order)
	}
}

func TestRegisterDuplicateRejected(t *testing.T) {
	hp := NewHookPack()
	fn := func() error { return nil }
	if err := hp.Register("a", fn); err != nil {
		t.Fatalf("first register failed: %v", err)
	}
	err := hp.Register("a", fn)
	if !errors.Is(err, ErrBadHook) {
		t.Fatalf("expected ErrBadHook, got %v", err)
	}
}

func TestRegisterEmptyNameRejected(t *testing.T) {
	hp := NewHookPack()
	err := hp.Register("", func() error { return nil })
	if !errors.Is(err, ErrBadHook) {
		t.Fatalf("expected ErrBadHook, got %v", err)
	}
}

func TestRunAllReturnsFirstErrorWithName(t *testing.T) {
	hp := NewHookPack()
	var calledC bool
	hp.Register("a", func() error { return nil })
	hp.Register("b", func() error { return errors.New("b failed") })
	hp.Register("c", func() error { calledC = true; return nil })
	err := hp.RunAll()
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "b") {
		t.Fatalf("error should contain 'b', got %v", err)
	}
	if calledC {
		t.Fatalf("hook c should not have been called")
	}
}

func TestPlatformTargets(t *testing.T) {
	targets := PlatformTargets()
	expected := []string{"darwin/arm64", "darwin/amd64", "linux/amd64", "linux/arm64"}
	if len(targets) != len(expected) {
		t.Fatalf("expected %d targets, got %d", len(expected), len(targets))
	}
	for i := range expected {
		if targets[i] != expected[i] {
			t.Fatalf("expected %s at index %d, got %s", expected[i], i, targets[i])
		}
	}
}
