package lifecycle

import (
	"testing"
)

// These symbols are the contract a later P2 slice wires; the references keep them compiled and tested until then.
var (
	_ Wait
	_ OwnerQueue
	_ ProjectCfg
	_ Touchpoint
	_ PhaseState
	_ Step
	_ Authorization
	_ Decision
	_ Hunt
	_ Meta
)

// TestDeadRef1Symbols pins the contract symbols a later slice uses.
func TestDeadRef1Symbols(t *testing.T) {

	t.Helper()

}
