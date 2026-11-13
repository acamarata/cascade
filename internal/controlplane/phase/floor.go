// Package phase defines the life-cycle phases of a project and provides
// functions to determine the current phase based on ticket statuses.
package phase

import "errors"

// PhaseState represents the current life-cycle phase of a project.
type PhaseState string

// Life-cycle phase constants.
const (
	// Seeded is the initial phase.
	Seeded PhaseState = "Seeded"
	// RTP is the Ready to Plan phase.
	RTP PhaseState = "RTP"
	// Planning is the planning phase. Planning is never derived; it is set by its own steps.
	Planning PhaseState = "Planning"
	// RTF is the Ready to Forge phase.
	RTF PhaseState = "RTF"
	// Forging is the forging phase.
	Forging PhaseState = "Forging"
	// RTB is the Ready to Build phase.
	RTB PhaseState = "RTB"
	// Building is the building phase.
	Building PhaseState = "Building"
	// RTD is the Ready to Deploy phase.
	RTD PhaseState = "RTD"
	// Deploying is the deploying phase. Deploying is never derived; it is set by its own steps.
	Deploying PhaseState = "Deploying"
	// Finished is the final phase.
	Finished PhaseState = "Finished"
)

// Floor determines the current phase based on the provided tickets and status word.
// A PHASE.md status word of "done", "closed", or "finished" maps to Finished.
// Unrecognised status words result in an error.
// Planning and Deploying are never derived; they are set by their own steps.
// Currently, this is a stub that returns an error for any input.
func Floor(tickets []string, statusWord string) (PhaseState, error) {
	return "", errors.New("not implemented")
}
