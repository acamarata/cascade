package crunch

import "testing"

// TestRecapLimitIsThree pins the reseed recap cap a later slice enforces.
func TestRecapLimitIsThree(t *testing.T) {
	if M1286RecapLimit != 3 {
		t.Fatalf("M1286RecapLimit = %d, want 3", M1286RecapLimit)
	}
}
