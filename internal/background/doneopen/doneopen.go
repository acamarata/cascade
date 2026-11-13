// Package doneopen models the doneopen background state, where work is
// complete but a follow-up reason explains why it remains open.
package doneopen

import (
	"errors"
	"fmt"
)

// Why identifies the reason a doneopen state is recorded.
type Why string

// Recognized doneopen reasons.
const (
	// ReviewNotesOpen indicates review notes remain open.
	ReviewNotesOpen Why = "review_notes_open"

	// NoReview indicates no review was performed.
	NoReview Why = "no_review"

	// NoRepoChange indicates no repository change was made.
	NoRepoChange Why = "no_repo_change"
)

// Status names a doneopen background state.
type Status string

// StatusDoneOpen is the status for a valid doneopen resolution.
const StatusDoneOpen Status = "doneopen"

// State records a doneopen background state and its reason.
type State struct {
	// Status is the resolved state status.
	Status Status `json:"status"`

	// Why is the reason for the doneopen state, when known.
	Why Why `json:"why,omitempty"`
}

// ErrInvalidWhy reports that a doneopen reason is not recognized.
var ErrInvalidWhy = errors.New("doneopen: invalid why")

// ResolveDoneOpen validates why and returns the corresponding doneopen
// status. It returns StatusDoneOpen for the recognized reasons. Every
// other value, including the empty string, returns an empty status and an
// error wrapping ErrInvalidWhy. ResolveDoneOpen does not mutate any State.
func ResolveDoneOpen(why Why) (Status, error) {
	switch why {
	case ReviewNotesOpen, NoReview, NoRepoChange:
		return StatusDoneOpen, nil
	}

	return Status(""), fmt.Errorf("%w %q", ErrInvalidWhy, why)
}
