// Package port provides core envelope types and error definitions.
package port

import (
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

// TestPortErrorInterface verifies every sentinel satisfies PortError with
// a non-empty code and message.
func TestPortErrorInterface(t *testing.T) {
	// The typed slice literal is the compile-time assertion that every
	// sentinel satisfies PortError.
	sentinels := []PortError{
		ErrInvalid, ErrNotFound, ErrConflict, ErrDenied,
		ErrUnavailable, ErrTimeout, ErrExhausted, ErrInternal,
	}
	for _, s := range sentinels {
		if s.Code() == "" {
			t.Errorf("sentinel %q has an empty Code", s.Error())
		}
		if s.Error() == "" {
			t.Error("expected non-empty Error string")
		}
	}
}

// TestEnvelopeVersioned verifies that Request handles versioning fields.
func TestEnvelopeVersioned(t *testing.T) {
	r := Request{Version: "v1"}
	if r.Version != "v1" {
		t.Errorf("unexpected Version: %s", r.Version)
	}
}

// TestResultDegradedNamesMissing verifies that degraded results populate Missing correctly
// and that Validate enforces valid Status values.
func TestResultDegradedNamesMissing(t *testing.T) {
	r := Result{
		Status:  "degraded",
		AsOf:    time.Now(),
		Missing: []string{"db", "cache"},
	}
	if err := r.Validate(); err != nil {
		t.Errorf("unexpected error for degraded status: %v", err)
	}
	if len(r.Missing) != 2 {
		t.Errorf("expected 2 missing items, got %d", len(r.Missing))
	}

	r2 := Result{Status: "unknown"}
	if err := r2.Validate(); err != ErrInvalid {
		t.Errorf("expected ErrInvalid for unknown status, got %v", err)
	}
}
