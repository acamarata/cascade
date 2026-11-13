// Package docket provides ticket scheduling primitives.
package docket

// SchedState represents the lifecycle state of a ticket.
type SchedState string

const (
	// SchedParked indicates a ticket that is parked and not actively progressing.
	SchedParked SchedState = "parked"
	// SchedPending indicates a ticket that is waiting to be started.
	SchedPending SchedState = "pending"
	// SchedRunning indicates a ticket that is currently in progress.
	SchedRunning SchedState = "running"
	// SchedDone indicates a ticket that has been completed.
	SchedDone SchedState = "done"
)

// SchedTicket represents a unit of work with dependencies.
type SchedTicket struct {
	ID    string
	State SchedState
	Deps  []string
}

// Result holds the outcome of a scheduling pass.
type Result struct {
	Schedulable []string
	Blocked     []string
}

// Schedule determines which pending tickets are schedulable based on their dependency states.
// When assumeDone is true, parked dependencies are treated as completed.
func Schedule(ts []SchedTicket, assumeDone bool) Result {
	stateByID := make(map[string]SchedState, len(ts))
	for _, t := range ts {
		stateByID[t.ID] = t.State
	}

	var r Result
	for _, t := range ts {
		if t.State != SchedPending {
			continue
		}
		blocked := false
		for _, depID := range t.Deps {
			st, ok := stateByID[depID]
			if !ok {
				blocked = true
				break
			}
			if st == SchedDone {
				continue
			}
			if st == SchedParked && assumeDone {
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
