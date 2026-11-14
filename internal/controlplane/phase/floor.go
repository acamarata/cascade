// Package phase defines the life-cycle phases of a project and provides
// functions to determine the current phase based on ticket statuses.
package phase

// PhaseState represents the current life-cycle phase of a project.
type PhaseState string //nolint:revive // name fixed by the P2 phase contract

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
