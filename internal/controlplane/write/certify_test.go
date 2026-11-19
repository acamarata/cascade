package write

import (
	"errors"
	"testing"
)

func TestCertifyWriteBeforeRead(t *testing.T) {
	s := Steps{
		Write: func(id string) error {
			if id != "M2153" {
				t.Errorf("expected id M2153, got %s", id)
			}
			return nil
		},
		Read: func(id string) (string, error) {
			return "data", nil
		},
		Validate: func(id, data string) (string, error) {
			return "ok", nil
		},
	}

	r, err := Certify("M2153", s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(r.Order) != 3 {
		t.Fatalf("expected 3 steps, got %d: %v", len(r.Order), r.Order)
	}
	if r.Order[0] != "write" || r.Order[1] != "read" || r.Order[2] != "validate" {
		t.Errorf("expected order [write read validate], got %v", r.Order)
	}
}

func TestCertifyWriteFailureStopsRead(t *testing.T) {
	readCalled := false
	validateCalled := false
	s := Steps{
		Write: func(id string) error {
			return errors.New("write failed")
		},
		Read: func(id string) (string, error) {
			readCalled = true
			return "", nil
		},
		Validate: func(id, data string) (string, error) {
			validateCalled = true
			return "", nil
		},
	}

	r, err := Certify("M2153", s)
	if err == nil || err.Error() != "write failed" {
		t.Fatalf("expected error 'write failed', got %v", err)
	}

	if readCalled {
		t.Errorf("read was called, but write failed")
	}
	if validateCalled {
		t.Errorf("validate was called, but write failed")
	}

	if len(r.Order) != 1 || r.Order[0] != "write" {
		t.Errorf("expected Order=[write], got %v", r.Order)
	}
}

func TestCertifyValidationAfterWrite(t *testing.T) {
	s := Steps{
		Write: func(id string) error {
			return nil
		},
		Read: func(id string) (string, error) {
			return "data", nil
		},
		Validate: func(id, data string) (string, error) {
			return "ok", nil
		},
	}

	r, err := Certify("M2153", s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if r.Output != "ok" {
		t.Errorf("expected output 'ok', got %q", r.Output)
	}
}
