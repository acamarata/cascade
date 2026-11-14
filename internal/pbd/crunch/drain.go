// Package crunch implements pure drain and relaunch policies for job scheduling.
package crunch

import "time"

// DrainMaxJobAge is the inclusive upper bound on the age of a running job during drain.
const DrainMaxJobAge = 10 * time.Minute

// DrainWidth enforces a pure drain policy by returning zero capacity regardless of the
// input width.
func DrainWidth(_ int) int {
	return 0
}

// DrainComplete reports whether a drain has finished. It returns true when there are no
// active jobs (empty or nil ages) or when every running job is younger than or exactly
// DrainMaxJobAge. Negative ages are invalid and yield false.
func DrainComplete(ages []time.Duration) bool {
	if len(ages) == 0 {
		return true
	}
	for _, age := range ages {
		if age < 0 || age > DrainMaxJobAge {
			return false
		}
	}
	return true
}

// AllowNewStart reports whether the scheduler may launch new jobs. It forbids new
// starts while draining is active and permits them only after the caller clears the
// draining flag.
func AllowNewStart(draining bool) bool {
	return !draining
}

// AllowRelaunch reports whether a failed job should be relaunched. Relaunch is
// permitted only when the engine signals that a fix is needed.
func AllowRelaunch(engineFixNeeded bool) bool {
	return engineFixNeeded
}
