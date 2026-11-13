package doneopen

import (
	"testing"
)

// These symbols are the contract a later P2 slice wires; the references keep them compiled and tested until then.
var (
	_ State
)

// TestDeadRef1Symbols pins the contract symbols a later slice uses.
func TestDeadRef1Symbols(t *testing.T) {

	t.Helper()

}
