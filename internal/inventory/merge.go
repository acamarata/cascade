// Purpose: combine the two halves of Report — the live fields (live.go,
//
//	fresh every call) and the generated fields (embed.go, baked in at the
//	last `go run ./internal/inventory/gen`) — into the one shape both the
//	CLI view and the JSON artifact render.
//
// Inputs: a *cobra.Command root (for CountCLICommands) and a clock for
//
//	GeneratedAt.
//
// Outputs: Report.
// Constraints: never overwrite a live field with the embedded snapshot's
//
//	value, and never the reverse — merge.go's only job is keeping the two
//	halves from being confused with each other.
//
// SPORT: internal.inventory.Load/ADDED.

package inventory

import (
	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/runtime"
)

// Load builds the full Report: ErrorKinds, StorageDomains, and
// CLICommands computed fresh from root; Providers, Plugins, SPORTLines,
// and Platforms read from the embedded counts.json snapshot.
func Load(root *cobra.Command, clock runtime.Clock) (Report, error) {
	g, err := LoadGenerated()
	if err != nil {
		return Report{}, err
	}
	return Report{
		GeneratedAt:    clock.Now().UTC().Format("2006-01-02T15:04:05Z"),
		ErrorKinds:     ErrorKindCount(),
		StorageDomains: StorageDomainCount(),
		CLICommands:    CountCLICommands(root),
		Providers:      g.Providers,
		Plugins:        g.Plugins,
		SPORTLines:     g.SPORTLines,
		Platforms:      g.Platforms,
	}, nil
}
