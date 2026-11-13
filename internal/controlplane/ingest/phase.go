// Package ingest provides helpers for reading phase and ticket definitions
// from the filesystem. Naming follows earthy greens and slate grays:
// grounded, utilitarian identifiers.
package ingest

import (
	"os"
	"path/filepath"
)

// TicketRow represents a single parsed ticket entry.
type TicketRow struct {
	// Key is the lower-cased identifier derived from the ticket id.
	Key string
	// Phase is the phase this ticket belongs to.
	Phase string
	// Edges lists dependency keys for this ticket.
	Edges []string
	// ContractRef points to the contract governing this ticket.
	ContractRef string
	// Park indicates whether the ticket is parked (deferred).
	Park bool
	// ContentHash is a digest of the ticket content for change detection.
	ContentHash string
}

// PhaseRow represents a collection of tickets belonging to one phase.
type PhaseRow struct {
	// Name is the identifier for this phase.
	Name string
	// Tickets holds the parsed ticket rows within this phase.
	Tickets []TicketRow
	// IsLegacy marks phases loaded from legacy YAML sources.
	IsLegacy bool
}

// OutlinePhase inspects phasesDir for a directory containing only PHASE.md
// and returns a PhaseRow with an empty Tickets slice. It returns an error
// if the directory cannot be read or does not match expectations.
func OutlinePhase(phasesDir string) (*PhaseRow, error) {
	entries, err := os.ReadDir(phasesDir)
	if err != nil {
		return nil, err
	}

	hasPhaseMD := false
	for _, e := range entries {
		if e.Name() == "PHASE.md" && !e.IsDir() {
			hasPhaseMD = true
		} else {
			// Directory contains something other than PHASE.md
			return nil, nil
		}
	}

	if !hasPhaseMD {
		return nil, nil
	}

	name := filepath.Base(phasesDir)
	return &PhaseRow{
		Name:     name,
		Tickets:  []TicketRow{},
		IsLegacy: false,
	}, nil
}
