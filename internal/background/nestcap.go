// Package background provides delegation admission control bounded by lane capacity.
package background

// Admit reports whether a delegation may proceed. It returns true while the
// number of active delegations is strictly less than the lane capacity,
// and false otherwise (the request must queue). Depth never changes the result.
func Admit(laneCapacity, active int) bool {
	return active < laneCapacity
}
