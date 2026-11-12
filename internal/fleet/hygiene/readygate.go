// Package hygiene provides fleet readiness gating utilities.
package hygiene

// MinClassSlots is the minimum number of slots required for readiness.
const MinClassSlots = 4

// ReadyGate tracks the unready state based on class slots.
type ReadyGate struct {
	// Unready is true when slots are below MinClassSlots.
	Unready bool
}

// Ready updates the Unready flag based on the given slots and returns whether the gate is ready.
// It sets Unready to true if slots is below MinClassSlots, and returns true if the gate is ready.
func (g *ReadyGate) Ready(slots int) bool {
	g.Unready = slots < MinClassSlots
	return !g.Unready
}
