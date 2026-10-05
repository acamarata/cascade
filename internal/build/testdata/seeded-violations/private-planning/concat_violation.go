// Package violation (concat_violation.go) is a seeded-violation fixture for
// the private-planning gate: a private path assembled by constant string
// concatenation, which the gate folds before matching.
package violation

// ProjectTruth names a private project directory by concatenation.
func ProjectTruth() string {
	return ".claude/" + "project" + "/STATE.md"
}
