// Package deadpkg is a seeded-violation fixture module for the dead-code
// gate (Art.10.5). Never built or linted — lives under testdata/.
package deadpkg

// DeadFunc is declared but referenced nowhere in this fixture module —
// the gate must report it.
func DeadFunc() int {
	return 1
}

// UsedFunc is declared here and referenced from internal/other in this
// same fixture module — the gate must NOT report it.
func UsedFunc() int {
	return 2
}
