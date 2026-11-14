package workerregistry

import (
	"errors"
	"testing"
)

// TestListShowsSupportStatus verifies that List returns workers with their
// correct support status in registration order.
func TestListShowsSupportStatus(t *testing.T) {
	reg := NewRegistry()

	w1 := Worker{Name: "w1", Status: "experimental"}
	w2 := Worker{Name: "w2", Status: "supported"}

	if err := reg.Register(w1); err != nil {
		t.Fatalf("register w1: %v", err)
	}
	if err := reg.Register(w2); err != nil {
		t.Fatalf("register w2: %v", err)
	}

	list := reg.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 workers, got %d", len(list))
	}
	if list[0].Name != "w1" || list[0].Status != "experimental" {
		t.Errorf("first worker: got name=%q status=%q, want name=w1 status=experimental", list[0].Name, list[0].Status)
	}
	if list[1].Name != "w2" || list[1].Status != "supported" {
		t.Errorf("second worker: got name=%q status=%q, want name=w2 status=supported", list[1].Name, list[1].Status)
	}
}

// TestRegisterUnknownStatusRejected verifies that a worker with an unknown
// status is rejected with ErrBadSupportStatus and is not stored.
func TestRegisterUnknownStatusRejected(t *testing.T) {
	reg := NewRegistry()

	w := Worker{Name: "beta-worker", Status: "beta"}
	err := reg.Register(w)
	if !errors.Is(err, ErrBadSupportStatus) {
		t.Fatalf("expected ErrBadSupportStatus, got %v", err)
	}

	list := reg.List()
	if len(list) != 0 {
		t.Errorf("expected empty list after rejected register, got %d workers", len(list))
	}
}
