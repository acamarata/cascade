package supervise

import "testing"

// TestSupervisorZeroValue verifies the zero value of the declared Supervisor type.
func TestSupervisorZeroValue(t *testing.T) {
	var s Supervisor
	if s != (Supervisor{}) {
		t.Fatalf("zero-value Supervisor = %#v, want %#v", s, Supervisor{})
	}
}
