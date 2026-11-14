package mod

import (
	"errors"
	"testing"
)

// conflictMock is a simple in-memory LandingResolver used by conflict tests.
type conflictMock struct {
	val string
	ok  bool
}

// Lookup returns the preconfigured value and presence flag.
func (c *conflictMock) Lookup(_ string) (string, bool) {
	return c.val, c.ok
}

// TestLandingConflictingTargetFails verifies that CheckLanding returns
// ErrConflictingLanding when a present landing target differs from "Crunch",
// even if both registries agree on the same wrong value.
func TestLandingConflictingTargetFails(t *testing.T) {
	cases := []struct {
		name string
		tVal string
		tOk  bool
		mVal string
		mOk  bool
	}{
		{"both agree Other", "Other", true, "Other", true},
		{"both agree empty", "", true, "", true},
		{"both agree lowercase crunch", "crunch", true, "crunch", true},
		{"both agree space prefix", " Crunch", true, " Crunch", true},
		{"table Other map Crunch", "Other", true, "Crunch", true},
		{"table Crunch map Other", "Crunch", true, "Other", true},
		{"table empty map Crunch", "", true, "Crunch", true},
		{"table Crunch map empty", "Crunch", true, "", true},
		{"table crunch map Crunch", "crunch", true, "Crunch", true},
		{"table space prefix map Crunch", " Crunch", true, "Crunch", true},
		{"table Other map crunch", "Other", true, "crunch", true},
		{"table empty map Other", "", true, "Other", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			table := &conflictMock{val: tc.tVal, ok: tc.tOk}
			m := &conflictMock{val: tc.mVal, ok: tc.mOk}

			err := CheckLanding(table, m)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, ErrConflictingLanding) {
				t.Fatalf("expected ErrConflictingLanding, got %v", err)
			}
		})
	}
}
