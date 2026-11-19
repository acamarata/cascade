// Package background provides background task helpers.
package background

import "time"

// StarveState tracks the last starvation note time per root.
type StarveState struct {
	// Last maps each root to the time its most recent note was emitted.
	Last map[string]time.Time
}

// NewStarveState returns an initialised StarveState.
func NewStarveState() *StarveState {
	return &StarveState{Last: make(map[string]time.Time)}
}

// Note represents a starvation inbox notification.
type Note struct {
	// Root is the identifier of the root that starved.
	Root string
	// At is the time the note was created.
	At time.Time
	// Text is the human-readable message.
	Text string
}

// EmitStarvation emits one starvation note per root when idle and nothing is queued.
// At most one note per root is emitted in any 3-hour window.
func EmitStarvation(state *StarveState, root string, idle, queued bool, now time.Time) (Note, bool) {
	if !idle || queued {
		return Note{}, false
	}
	last, ok := state.Last[root]
	if ok && now.Sub(last) < 3*time.Hour {
		return Note{}, false
	}
	note := Note{
		Root: root,
		At:   now,
		Text: "starvation detected",
	}
	state.Last[root] = now
	return note, true
}
