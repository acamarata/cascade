// Package port provides core envelope types and error definitions.
package port

import (
	"errors"
	"testing"
	"time"
)

// TestRequestFields verifies that Request has the required fields.
func TestRequestFields(t *testing.T) {
	r := Request{
		Version:        "1",
		IdempotencyKey: "key",
		Caller:         "caller",
		Deadline:       time.Now(),
	}
	if r.Version != "1" {
		t.Errorf("unexpected Version: %s", r.Version)
	}
	if r.IdempotencyKey != "key" {
		t.Errorf("unexpected IdempotencyKey: %s", r.IdempotencyKey)
	}
	if r.Caller != "caller" {
		t.Errorf("unexpected Caller: %s", r.Caller)
	}
	if r.Deadline.IsZero() {
		t.Error("unexpected zero Deadline")
	}
}

// TestResultFields verifies that Result has the required fields.
func TestResultFields(t *testing.T) {
	r := Result{
		Status:  "ok",
		AsOf:    time.Now(),
		Missing: []string{"a"},
	}
	if r.Status != "ok" && r.Status != "partial" && r.Status != "degraded" {
		t.Errorf("unexpected Status: %s", r.Status)
	}
	if r.AsOf.IsZero() {
		t.Error("unexpected zero AsOf")
	}
	if len(r.Missing) != 1 {
		t.Errorf("unexpected Missing length: %d", len(r.Missing))
	}
}

// TestPortErrorInterface verifies compile-time assertions for PortError.
func TestPortErrorInterface(t *testing.T) {
	var _ PortError = ErrInvalid
	var _ PortError = ErrNotFound
	var _ PortError = ErrConflict
	var _ PortError = ErrDenied
	var _ PortError = ErrUnavailable
	var _ PortError = ErrTimeout
	var _ PortError = ErrExhausted
	var _ PortError = ErrInternal

	sentinels := []PortError{
		ErrInvalid, ErrNotFound, ErrConflict, ErrDenied,
		ErrUnavailable, ErrTimeout, ErrExhausted, ErrInternal,
	}
	for _, s := range sentinels {
		if s.Code() != "" {
			t.Errorf("expected empty Code, got %s", s.Code())
		}
		if s.Retryable() {
			t.Error("expected Retryable to be false")
		}
		if s.Error() == "" {
			t.Error("expected non-empty Error string")
		}
	}
}

// TestErrNotImplemented verifies the ErrNotImplemented sentinel.
func TestErrNotImplemented(t *testing.T) {
	if !errors.Is(ErrNotImplemented, errors.New("not implemented")) {
		// errors.Is compares by value for errors.New, so we check the string
	}
	if ErrNotImplemented.Error() != "not implemented" {
		t.Errorf("unexpected error message: %s", ErrNotImplemented.Error())
	}
}
