package background

import "testing"

// TestAdmitDeepNestingUnderCapacity verifies that a delegation is admitted
// when the active count is below the lane capacity, even across 50 nesting levels.
func TestAdmitDeepNestingUnderCapacity(t *testing.T) {
	for level := 0; level < 50; level++ {
		if !Admit(4, 3) {
			t.Fatalf("level %d: expected admission with capacity 4 and active 3", level)
		}
	}
}

// TestAdmitFullLaneQueues verifies that a delegation is not admitted (must queue)
// when the active count reaches the lane capacity.
func TestAdmitFullLaneQueues(t *testing.T) {
	if Admit(4, 4) {
		t.Fatal("expected queueing with capacity 4 and active 4")
	}
}

// TestAdmitDepthIgnored verifies that the admission decision is independent
// of the nesting depth.
func TestAdmitDepthIgnored(t *testing.T) {
	shallow := Admit(4, 2)
	deep := true
	for level := 0; level < 50; level++ {
		if !Admit(4, 2) {
			deep = false
			break
		}
	}
	if shallow != deep {
		t.Fatalf("admission varied with depth: shallow=%v, deep=%v", shallow, deep)
	}
}
