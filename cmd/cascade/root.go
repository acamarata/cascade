// Purpose: cobra root command definition and persistent global flags.
// Inputs:  process args, parsed by cobra into GlobalFlags.
// Outputs: the constructed root *cobra.Command tree (root + version + completion
//
//	at this ticket; later tickets mount further command groups per
//	07-CLI-COMMAND-TREE.md against this same root).
//
// Constraints: pure CLI wiring only — no business logic (06-FORGE-SPEC §2).
//
//	Global flags are declared exactly per 07-CLI-COMMAND-TREE.md §global-flags:
//	--json --profile --config -q/--quiet -v/--verbose.
//
// SPORT: cmd/cascade — cobra-root, global-flags, version, completions.
package main

import (
	"errors"

	"github.com/spf13/cobra"
)

// GlobalFlags holds the persistent flag values shared by every subcommand.
// Subcommands read this struct directly (same package) or, from outside the
// package, via cmd.Root().PersistentFlags().
type GlobalFlags struct {
	// JSON requests the versioned JSON envelope output contract (D/S-06.T5).
	JSON bool
	// Profile selects a named config profile.
	Profile string
	// Config overrides the config file path.
	Config string
	// Quiet suppresses progress output.
	Quiet bool
	// Verbose increases log verbosity.
	Verbose bool
}

var globalFlags GlobalFlags

// newRootCmd constructs the cascade root command with its persistent global
// flags and mounts the subcommands owned by this ticket. Later tickets add
// further subcommands (e.g. D/S-06.T2's `daemon` group) against this same
// root without needing to change this function's shape.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "cascade",
		Short:         "Cascade: a local-first AI agent runtime",
		Long:          "Cascade is a local-first AI agent runtime: one binary that is both\nthe CLI surface and, via \"cascade daemon run\", the long-lived daemon.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if globalFlags.Quiet && globalFlags.Verbose {
				return errors.New("--quiet and --verbose are mutually exclusive")
			}
			return nil
		},
	}

	flags := root.PersistentFlags()
	flags.BoolVar(&globalFlags.JSON, "json", false, "emit output as a versioned JSON envelope")
	flags.StringVar(&globalFlags.Profile, "profile", "", "select a named config profile")
	flags.StringVar(&globalFlags.Config, "config", "", "override the config file path")
	flags.BoolVarP(&globalFlags.Quiet, "quiet", "q", false, "suppress progress output")
	flags.BoolVarP(&globalFlags.Verbose, "verbose", "v", false, "increase log verbosity")

	root.AddCommand(newVersionCmd())
	root.AddCommand(newCompletionCmd(root))

	return root
}
