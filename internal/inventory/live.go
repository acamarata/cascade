// Purpose: the three Report fields computed FRESH on every call, never
//
//	baked into counts.json, because the same binary that reports them also
//	carries the artifact they are derived from (the frozen taxonomy, the
//	closed domain enumeration, and the cobra tree it just built). These are
//	the fields that can NEVER disagree with reality: there is no
//	regeneration step between the artifact changing and the count
//	reflecting it.
//
// Inputs: pkg/cascade.AllKinds, internal/storage.AllDomains, and a
//
//	*cobra.Command the caller already built (cmd/cascade owns construction;
//	this package only counts).
//
// Outputs: three int counts.
// Constraints: no tree access, no I/O — pure functions over in-process
//
//	data, so they run identically in a dev checkout and an installed
//	binary with no source tree at all.
//
// SPORT: internal.inventory.ErrorKindCount/ADDED,
//
//	internal.inventory.StorageDomainCount/ADDED,
//	internal.inventory.CountCLICommands/ADDED.

package inventory

import (
	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrorKindCount returns the frozen taxonomy's member count, computed from
// cascade.AllKinds() — the exact slice pkg/cascade's own exhaustiveness
// tests and every wire table (CLI exit code, JSON-RPC code) are built
// against. There is no second copy of "14" anywhere in this package.
func ErrorKindCount() int { return len(cascade.AllKinds()) }

// StorageDomainCount returns the closed domain enumeration's member count,
// computed from storage.AllDomains — the exact slice Bootstrap iterates to
// create each domain's anchor table.
func StorageDomainCount() int { return len(storage.AllDomains) }

// CountCLICommands walks root's real command tree (root plus every
// transitively-registered subcommand) and returns the total. Called with
// the SAME *cobra.Command the running binary dispatches on
// (cmd.Root() from inside a RunE), so this count can never disagree with
// what `cascade --help` actually lists: it IS that tree, not a
// re-derivation of it.
//
// hidden and deprecated commands count too — this answers "how many
// commands does the binary carry", not "how many are advertised".
func CountCLICommands(root *cobra.Command) int {
	if root == nil {
		return 0
	}
	total := 1 // root itself
	for _, c := range root.Commands() {
		total += CountCLICommands(c)
	}
	return total
}
