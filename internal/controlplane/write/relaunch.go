// Package write provides control-plane write helpers including relaunch recommendations.
package write

import "time"

// Recommendation describes a suggested action for the supervisor.
type Recommendation struct {
	// Action is the recommended operation to perform.
	Action string
}

// Window tracks the relaunch delay timer after a supervisor_dead finding.
type Window struct {
	// Started is the time the window was opened.
	Started time.Time
	// Active indicates whether the window is still waiting for a relaunch.
	Active bool
	// Relaunched indicates whether a relaunch has already occurred.
	Relaunched bool
}

// RelaunchDelay is the duration to wait before recommending a relaunch.
const RelaunchDelay = 5 * time.Minute

// StartWindow creates a new active Window starting at the given time.
func StartWindow(now time.Time) Window {
	return Window{
		Started:    now,
		Active:     true,
		Relaunched: false,
	}
}

// Recommend returns a relaunch recommendation if the window is active,
// no relaunch has occurred, and at least RelaunchDelay has elapsed since
// the window started. The boolean return value indicates whether a
// recommendation should be acted upon.
func Recommend(w Window, now time.Time) (Recommendation, bool) {
	if !w.Active || w.Relaunched {
		return Recommendation{}, false
	}
	if now.Sub(w.Started) < RelaunchDelay {
		return Recommendation{}, false
	}
	return Recommendation{Action: "relaunch_saved_spec"}, true
}
