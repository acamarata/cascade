// Package lifecycle defines the phase lifecycle states and their legal transitions.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

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

// legalMoves encodes the allowed transitions between phases.
var legalMoves = map[Phase][]Phase{
	Seeded:    {RTP, RTB},
	RTP:       {Planning, RTB},
	Planning:  {RTF, RTP},
	RTF:       {Forging, RTP},
	Forging:   {RTB, RTF},
	RTB:       {Building, RTF},
	Building:  {RTD, RTB},
	RTD:       {Deploying},
	Deploying: {Finished, RTD},
}

// Transition checks if moving from one Phase to another is legal.
// It returns nil when to is in legalMoves[from], otherwise returns an error
// wrapping ErrIllegalTransition.
func Transition(from, to Phase) error {
	for _, allowed := range legalMoves[from] {
		if to == allowed {
			return nil
		}
	}
	return fmt.Errorf("%w: %s->%s", ErrIllegalTransition, from, to)
}

// Force represents a fact observed in git or a live supervisor forcing a stage.
// It logs at Info level with message prefix "observed" followed by the target phase
// and returns nil. Force overrides normal flow without transition validation.
func Force(ctx context.Context, logger *slog.Logger, to Phase) error {
	logger.InfoContext(ctx, "observed forcing phase to "+string(to))
	return nil
}
