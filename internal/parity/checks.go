// Package parity provides cutover parity checks.
package parity

// Check represents a single cutover check.
type Check struct {
	Name     string
	Pass     bool
	Evidence string
}

// RequiredChecks returns the required checks in the exact order they must be performed.
func RequiredChecks() []string {
	return []string{"memory", "safety", "lifecycle", "cutover-rehearsal"}
}

// MissingChecks returns the required checks that are either absent or not passing,
// in the order defined by RequiredChecks.
func MissingChecks(got []Check) []string {
	passed := make(map[string]bool)
	for _, c := range got {
		if c.Pass {
			passed[c.Name] = true
		}
	}

	var missing []string
	for _, name := range RequiredChecks() {
		if !passed[name] {
			missing = append(missing, name)
		}
	}
	return missing
}
