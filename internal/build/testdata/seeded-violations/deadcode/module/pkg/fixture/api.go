// Package fixture is a seeded-violation fixture module for the pkg/
// SDK-intent half of the dead-code gate.
package fixture

// NoIntentUnused is unused anywhere in this fixture module and carries no
// SDK-intent marker — the gate must report it.
func NoIntentUnused() int {
	return 1
}

// ExportedWithIntent is unused anywhere in this fixture module but is
// documented as deliberate, forward-declared public API.
//
// SDK-INTENT: reserved for a future consumer; do not remove.
func ExportedWithIntent() int {
	return 2
}
