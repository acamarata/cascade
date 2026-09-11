// Purpose: `cascade doctor counts` — the CLI half of the owner's counts
//
//	surface (internal/inventory): "for things like number of something
//	... we need a good SPORT model or where to see as we develop because
//	this changes all the time." Split from doctor.go per R-14.117's
//	same-package sibling-file convention, to keep doctor.go under Art.10.3.
//
// Inputs: the same doctorDeps doctor.go injects; the real root *cobra.Command
//
//	this process built (cmd.Root()), so CLICommands is counted from the
//	exact tree this binary dispatches on, never a re-derivation of it.
//
// Outputs: process output via internal/output.Writer; internal/inventory.Report,
//
//	human table by default, the versioned --json envelope with --json.
//
// Constraints: read-only — never prompts, never writes. The generated
//
//	quarter of Report (providers/plugins/sport-lines/platforms) is whatever
//	the embedded counts.json currently holds; a stale counts.json is a
//	drift-gate failure (internal/build/countsdriftgate.go), not this
//	command's problem to detect.
//
// SPORT: cmd/cascade/doctor (ADD - counts subcommand).
package main

import (
	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/inventory"
	"github.com/acamarata/cascade/pkg/cascade"
)

// newDoctorCountsCmd builds `cascade doctor counts`.
func newDoctorCountsCmd(deps doctorDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "counts",
		Short: "Show derived counts (error kinds, storage domains, CLI commands, providers, plugins, platforms)",
		Long: "Report the counts this tree states in prose in a dozen places, computed\n" +
			"from the same artifact the program itself uses rather than a hand-\n" +
			"maintained number that can silently disagree with reality.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDoctorCounts(cmd, deps)
		},
	}
}

// runDoctorCounts assembles the Report and renders it.
func runDoctorCounts(cmd *cobra.Command, deps doctorDeps) error {
	report, err := inventory.Load(cmd.Root(), deps.Clock)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "cascade doctor counts: load")
	}
	return doctorOutputWriter(cmd).Result(report)
}
