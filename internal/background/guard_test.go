package background

import (
	"errors"
	"testing"
)

// TestAllowNormalPush verifies that a standard push command without force flags is allowed.
func TestAllowNormalPush(t *testing.T) {
	guard := NewGuard(func(args []string) error {
		return nil
	})

	err := guard.Evaluate([]string{"push", "origin", "main"})
	if err != nil {
		t.Errorf("TestAllowNormalPush: expected nil, got %v", err)
	}
}

// TestBlockForcePushFlag verifies that a push with --force flag is rejected with M1478.
func TestBlockForcePushFlag(t *testing.T) {
	guard := NewGuard(func(args []string) error {
		return nil
	})

	err := guard.Evaluate([]string{"push", "--force", "origin", "main"})
	if err == nil {
		t.Errorf("TestBlockForcePushFlag: expected M1478 error, got nil")
	}
	if err != nil && err.Error() != "M1478: force-push rejected or pre-push hook failed" {
		t.Errorf("TestBlockForcePushFlag: expected M1478 error, got %v", err)
	}
}

// TestBlockForcePushWithLease verifies that a push with --force-with-lease flag is rejected with M1478.
func TestBlockForcePushWithLease(t *testing.T) {
	guard := NewGuard(func(args []string) error {
		return nil
	})

	err := guard.Evaluate([]string{"push", "--force-with-lease", "origin", "main"})
	if err == nil {
		t.Errorf("TestBlockForcePushWithLease: expected M1478 error, got nil")
	}
	if err != nil && err.Error() != "M1478: force-push rejected or pre-push hook failed" {
		t.Errorf("TestBlockForcePushWithLease: expected M1478 error, got %v", err)
	}
}

// TestPrePushHookPasses verifies that a push with a passing pre-push hook is allowed.
func TestPrePushHookPasses(t *testing.T) {
	guard := NewGuard(func(args []string) error {
		return nil
	})

	err := guard.Evaluate([]string{"push", "origin", "main"})
	if err != nil {
		t.Errorf("TestPrePushHookPasses: expected nil, got %v", err)
	}
}

// TestPrePushHookFailsReturnsM1478 verifies that a push with a failing pre-push hook returns M1478.
func TestPrePushHookFailsReturnsM1478(t *testing.T) {
	guard := NewGuard(func(args []string) error {
		return errors.New("hook failed")
	})

	err := guard.Evaluate([]string{"push", "origin", "main"})
	if err == nil {
		t.Errorf("TestPrePushHookFailsReturnsM1478: expected M1478 error, got nil")
	}
	if err != nil && err.Error() != "M1478: force-push rejected or pre-push hook failed" {
		t.Errorf("TestPrePushHookFailsReturnsM1478: expected M1478 error, got %v", err)
	}
}
