package conform

import (
	"errors"
	"testing"
)

// TestSuiteStub verifies that the stub Suite.Run returns ErrNotImplemented
// and that the sentinel carries the package-qualified message.
func TestSuiteStub(t *testing.T) {
	if ErrNotImplemented == nil {
		t.Fatal("ErrNotImplemented is nil")
	}
	if got, want := ErrNotImplemented.Error(), "conform: operation unavailable"; got != want {
		t.Errorf("ErrNotImplemented.Error() = %q, want %q", got, want)
	}
	s := &Suite{}
	if err := s.Run(); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Suite.Run() = %v, want ErrNotImplemented", err)
	}
}
