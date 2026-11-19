// Package background provides progress watch evaluation logic.
package background

import "time"

// WatchDays is the number of days the watch remains silent before filing incidents.
const WatchDays = 7

// RefileHours is the number of hours after filing before an unresolved incident is refiled.
const RefileHours = 24

// ActionType represents the kind of action returned by Evaluate.
type ActionType int

const (
	// Record indicates a silent observation with no incident filed.
	Record ActionType = iota
	// File indicates a new incident should be filed.
	File
	// Refile indicates an existing unresolved incident should be refiled.
	Refile
)

// RefileAction describes a single action produced by Evaluate.
type RefileAction struct {
	// Type is the kind of action to perform.
	Type ActionType
}

// Finding represents an open issue discovered during the watch window.
type Finding struct {
	// Resolved indicates whether the finding has been resolved.
	Resolved bool
	// FiledAt is the time the finding was first filed as an incident.
	FiledAt time.Time
}

// State holds the current watch state passed to Evaluate.
type State struct {
	// FirstPass is the time of the initial observation.
	FirstPass time.Time
	// Findings is the list of current findings.
	Findings []Finding
}

// Evaluate inspects the given state at time now and returns the appropriate actions.
func Evaluate(state interface{}, now time.Time) []RefileAction {
	s, ok := state.(State)
	if !ok {
		return nil
	}

	var actions []RefileAction

	silentEnd := s.FirstPass.Add(time.Duration(WatchDays) * 24 * time.Hour)

	if now.Before(silentEnd) {
		actions = append(actions, RefileAction{Type: Record})
		return actions
	}

	for _, f := range s.Findings {
		if f.Resolved {
			continue
		}
		if f.FiledAt.IsZero() {
			actions = append(actions, RefileAction{Type: File})
		} else {
			refileAfter := f.FiledAt.Add(time.Duration(RefileHours) * time.Hour)
			if !now.Before(refileAfter) {
				actions = append(actions, RefileAction{Type: Refile})
			}
		}
	}

	return actions
}
