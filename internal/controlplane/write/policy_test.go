package write

import (
	"errors"
	"testing"
)

func TestNilPolicyFuncFailsClosed(t *testing.T) {
	c, err := NewConductor(nil)
	if c != nil {
		t.Errorf("NewConductor(nil) got conductor %v, want nil", c)
	}
	if !errors.Is(err, ErrD1147) {
		t.Errorf("NewConductor(nil) error = %v, want errors.Is(ErrD1147)", err)
	}
	if err != nil && err.Error() != "D1147: conductor requires an injected PolicyFunc" {
		t.Errorf("NewConductor(nil) error text = %q, want %q", err.Error(), "D1147: conductor requires an injected PolicyFunc")
	}

	var cNil *Conductor
	ok, err := cNil.Decide("some-subject")
	if ok {
		t.Errorf("nil receiver Decide() = true, want false")
	}
	if err == nil {
		t.Errorf("nil receiver Decide() error = nil, want error")
	}

	var cZero Conductor
	ok, err = cZero.Decide("some-subject")
	if ok {
		t.Errorf("zero-value conductor Decide() = true, want false")
	}
	if err == nil {
		t.Errorf("zero-value conductor Decide() error = nil, want error")
	}
}
