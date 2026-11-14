package ingest

import (
	"os"
)

// ParseCurrentYAML reads the legacy current.yaml file at the given path
// and returns exactly one PhaseRow with IsLegacy set to true.
// This is a pure parsing helper that does not perform file I/O for plans.
func ParseCurrentYAML(path string) ([]PhaseRow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// For legacy phases, we return exactly one PhaseRow marked as legacy.
	name := "legacy"
	if len(data) > 0 {
		// Simple parsing: just mark as legacy, actual field parsing would be more complex
		name = "legacy"
	}

	return []PhaseRow{
		{
			Name:     name,
			Tickets:  []TicketRow{},
			IsLegacy: true,
		},
	}, nil
}
