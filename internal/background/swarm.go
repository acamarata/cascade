// Package background provides swarm event types and pure mappings.
package background

import "time"

// Event represents a stage event emitted during swarm processing.
type Event struct {
	Name  string    `json:"name,omitempty"`
	Stage string    `json:"stage,omitempty"`
	Kind  string    `json:"kind"`
	At    time.Time `json:"at,omitempty"`
}

// Outcome represents the result status of an attempt.
type Outcome struct {
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// Attempt represents a single verification attempt.
type Attempt struct {
	N          int
	VerifyTail string
	Err        string
}

// SwarmResult aggregates events, outcome, and the next brief identifier.
type SwarmResult struct {
	Events    []Event
	Outcome   Outcome
	NextBrief string
}

// MapAttempt is a total pure mapping from an Attempt to an Event and Outcome.
func MapAttempt(a Attempt) (Event, Outcome) {
	ev := Event{Stage: "author", Kind: "stage"}
	if a.Err == "" {
		return ev, Outcome{Status: "done"}
	}
	return ev, Outcome{Status: "needs-owner", Detail: a.Err}
}
