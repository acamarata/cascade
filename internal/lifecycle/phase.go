package lifecycle

import "errors"

// Phase represents a stage in the Conductor Lifecycle.
type Phase string

const (
	// Seeded is the initial phase.
	Seeded Phase = "Seeded"
	// RTP is a phase.
	RTP Phase = "RTP"
	// RTB is a phase.
	RTB Phase = "RTB"
	// Planning is a phase.
	Planning Phase = "Planning"
	// RTF is a phase.
	RTF Phase = "RTF"
	// Forging is a phase.
	Forging Phase = "Forging"
	// Building is a phase.
	Building Phase = "Building"
	// RTD is a phase.
	RTD Phase = "RTD"
	// Deploying is a phase.
	Deploying Phase = "Deploying"
	// Finished is the final phase.
	Finished Phase = "Finished"
)

// ErrIllegalTransition is returned when a phase transition is not allowed.
var ErrIllegalTransition = errors.New("illegal phase transition")
