package mod

import (
	"errors"
	"strings"
	"testing"
)

func mustRegister(t *testing.T, hp *HookPack, name string, fn func() error) {
	t.Helper()
	if err := hp.Register(name, fn); err != nil {
		t.Fatalf("register %q failed: %v", name, err)
	}
}

func TestRunAllExecutesInOrder(t *testing.T) {
	hp := NewHookPack()
	var order []string
	mustRegister(t, hp, "a", func() error { order = append(order, "a"); return nil })
	mustRegister(t, hp, "b", func() error { order = append(order, "b"); return nil })
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
	mustRegister(t, hp, "a", func() error { return nil })
	mustRegister(t, hp, "b", func() error { return errors.New("b failed") })
	mustRegister(t, hp, "c", func() error { calledC = true; return nil })
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
