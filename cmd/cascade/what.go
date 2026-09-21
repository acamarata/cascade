// Purpose: `cascade what <query>` — the hidden top-level alias for
// `cascade recall what <query>` (07-CLI-COMMAND-TREE §note-2, T0 ruling:
// "`cascade what` kept as hidden alias of `recall what` (owner
// ergonomics)."), registered from root.go's mountSubcommands. Built the
// same way the two existing hidden top-level aliases in this package are
// (fleet.go's mountFleetSessionsAlias, fleet_journal.go's
// mountFleetJournalAlias): call the exact command constructor the primary
// surface itself calls — newRecallWhatCmd, cmd/cascade/recall_what.go's
// own function, already mounted under `recall` by newRecallCmd — a
// second time, then mark the result Hidden. No RunE, no flag, no
// rendering is written here or anywhere else a second time; both trees
// run the identical closure body newRecallWhatCmd builds.
//
// Inputs: none of its own — same positional query, same recallDeps
// injection recall.go/recall_what.go already established (V/S-47.T1).
//
// Outputs: byte-identical to `cascade recall what`'s, for the same input
// and the same deps, because both commands execute the same RunE source
// (what_test.go's TestWhatAliasParity drives both through the real cobra
// tree with one shared fake recallDeps and diffs stdout/stderr/exit).
//
// Constraints: Hidden: true so the alias never appears in `cascade
// --help` or shell completions — 06-FORGE-SPEC §5.8's automation-parity
// rule treats an alias as the same contract surface, never a second one
// to advertise. No platform-specific code (Art.5): this file imports
// nothing but cobra.
//
// SPORT: cmd.cascade.cmd.what (ADD, P1-E22-W5-S47-T5).
package main

import "github.com/spf13/cobra"

// mountWhatCmd attaches the hidden `cascade what <query>` alias to root,
// mirroring mountFleetSessionsAlias/mountFleetJournalAlias's established
// pattern for a hidden top-level alias of a nested subcommand.
func mountWhatCmd(root *cobra.Command) {
	root.AddCommand(newWhatCmd(productionRecallDeps()))
}

// newWhatCmd builds the hidden alias by calling newRecallWhatCmd — the
// same function `cascade recall what` itself calls (recall.go's
// newRecallCmd) — a second time. Use and the positional-arg contract
// already come out identical from that shared constructor; Hidden is the
// one field this function sets, which is what makes the result an alias
// rather than a second mount of the same visible command.
func newWhatCmd(deps recallDeps) *cobra.Command {
	cmd := newRecallWhatCmd(deps)
	cmd.Use = "what <query>"
	cmd.Hidden = true
	return cmd
}
