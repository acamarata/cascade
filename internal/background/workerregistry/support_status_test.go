package workerregistry

import (
	"errors"
	"testing"
)

func TestRegisterExperimentalDefaultsStatus(t *testing.T) {
	r := NewRegistry()
	w := Worker{Name: "w1", Experimental: true}
	err := r.Register(w)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	list := r.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 worker, got %d", len(list))
	}
	if list[0].Status != "experimental" {
		t.Errorf("expected status 'experimental', got %q", list[0].Status)
	}
	if !list[0].Experimental {
		t.Errorf("expected Experimental flag to be preserved")
	}
}

func TestRegisterPlainWorkerIsSupported(t *testing.T) {
	r := NewRegistry()
	w := Worker{Name: "w2"}
	err := r.Register(w)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	list := r.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 worker, got %d", len(list))
	}
	if list[0].Status != "supported" {
		t.Errorf("expected status 'supported', got %q", list[0].Status)
	}
}

func TestRegisterValidation(t *testing.T) {
	r := NewRegistry()

	// Empty name
	err := r.Register(Worker{})
	if !errors.Is(err, ErrInvalidWorker) {
		t.Errorf("expected ErrInvalidWorker for empty name, got %v", err)
	}

	// Bad status
	err = r.Register(Worker{Name: "w3", Status: "unknown"})
	if !errors.Is(err, ErrBadSupportStatus) {
		t.Errorf("expected ErrBadSupportStatus for bad status, got %v", err)
	}

	// Duplicate
	if err = r.Register(Worker{Name: "w4"}); err != nil {
		t.Fatalf("Register w4: %v", err)
	}
	err = r.Register(Worker{Name: "w4"})
	if !errors.Is(err, ErrDuplicateWorker) {
		t.Errorf("expected ErrDuplicateWorker for duplicate name, got %v", err)
	}

	// Nil receiver
	var nilReg *Registry
	err = nilReg.Register(Worker{Name: "w5"})
	if !errors.Is(err, ErrInvalidWorker) {
		t.Errorf("expected ErrInvalidWorker for nil receiver, got %v", err)
	}

	// Explicit valid status wins
	r2 := NewRegistry()
	if err = r2.Register(Worker{Name: "w6", Experimental: true, Status: "supported"}); err != nil {
		t.Fatalf("Register w6: %v", err)
	}
	list := r2.List()
	if list[0].Status != "supported" {
		t.Errorf("expected explicit status 'supported', got %q", list[0].Status)
	}
}
