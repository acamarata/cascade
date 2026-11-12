package hygiene

import "testing"

func TestReadyBlockedBelowFloor(t *testing.T) {
	g := &ReadyGate{}
	if g.Ready(MinClassSlots - 1) {
		t.Errorf("expected not ready below floor")
	}
}

func TestReadyAllowedAtFloor(t *testing.T) {
	g := &ReadyGate{}
	if !g.Ready(MinClassSlots) {
		t.Errorf("expected ready at floor")
	}
}

func TestReadyAllowedAboveFloor(t *testing.T) {
	g := &ReadyGate{}
	if !g.Ready(MinClassSlots + 1) {
		t.Errorf("expected ready above floor")
	}
}

func TestUnreadyFlagStaysSetBelowFloor(t *testing.T) {
	g := &ReadyGate{}
	g.Ready(MinClassSlots - 1)
	if !g.Unready {
		t.Errorf("expected Unready flag to be set below floor")
	}
}
