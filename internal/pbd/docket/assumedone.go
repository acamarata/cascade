// Package docket provides ticket scheduling primitives.
package docket

// State represents the lifecycle state of a ticket.
type State string

const (
	// Parked indicates a ticket that is parked and not actively progressing.
	Parked State = "parked"
	// Pending indicates a ticket that is waiting to be started.
	Pending State = "pending"
	// Running indicates a ticket that is currently in progress.
	Running State = "running"
	// Done indicates a ticket that has been completed.
	Done State = "done"
)

// Ticket represents a unit of work with dependencies.
type Ticket struct {
	ID    string
	State State
	Deps  []string
}

// Result holds the outcome of a scheduling pass.
type Result struct {
	Schedulable []string
	Blocked     []string
}

// Schedule determines which pending tickets are schedulable based on their dependency states.
// When assumeDone is true, parked dependencies are treated as completed.
func Schedule(ts []Ticket, assumeDone bool) Result {
	stateByID := make(map[string]State, len(ts))
	for _, t := range ts {
		stateByID[t.ID] = t.State
	}

	var r Result
	for _, t := range ts {
		if t.State != Pending {
			continue
		}
		blocked := false
		for _, depID := range t.Deps {
			st, ok := stateByID[depID]
			if !ok {
				blocked = true
				break
			}
			if st == Done {
				continue
			}
			if st == Parked && assumeDone {
				continue
			}
			blocked = true
			break
		}
		if blocked {
			r.Blocked = append(r.Blocked, t.ID)
		} else {
			r.Schedulable = append(r.Schedulable, t.ID)
		}
	}
	return r
}
