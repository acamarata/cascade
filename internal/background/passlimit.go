// Package background provides background task management utilities.
package background

// PassLimit computes the number of tasks a pass may start.
// When the CPU reader returns an error, the limit is 1.
// When the read succeeds, the limit follows the headroom value,
// never below 0.
func PassLimit(read func() (float64, error), headroom func(cpu float64) int) int {
	cpu, err := read()
	if err != nil {
		return 1
	}
	limit := headroom(cpu)
	if limit < 0 {
		return 0
	}
	return limit
}
