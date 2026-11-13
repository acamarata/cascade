package consult

import "testing"

// TestA1OwnerAllowed verifies that the owner can invoke A1 for task T0.
func TestA1OwnerAllowed(t *testing.T) {
	res := CheckAccess(Owner, T0, A1)
	if !res.Allowed {
		t.Errorf("expected owner T0 A1 to be allowed, got rejected with code %s", res.Code)
	}
}

// TestA1NonOwnerRejected verifies that non-owners are rejected with T0417 when invoking A1.
func TestA1NonOwnerRejected(t *testing.T) {
	res := CheckAccess(Caller("agent-x"), T0, A1)
	if res.Allowed {
		t.Error("expected agent-x T0 A1 to be rejected, but it was allowed")
	}
	if res.Code != "T0417" {
		t.Errorf("expected error code T0417, got %s", res.Code)
	}
}

// TestA1OtherTaskRejected verifies that the owner is rejected with T0417 when invoking A1 for a non-T0 task.
func TestA1OtherTaskRejected(t *testing.T) {
	res := CheckAccess(Owner, Task("T1"), A1)
	if res.Allowed {
		t.Error("expected owner T1 A1 to be rejected, but it was allowed")
	}
	if res.Code != "T0417" {
		t.Errorf("expected error code T0417, got %s", res.Code)
	}
}
