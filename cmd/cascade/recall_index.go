// Purpose: `cascade recall index rebuild|verify|migrate|update`
// (07-CLI-COMMAND-TREE §recall, extended by R-16.8) — the index lifecycle
// verbs' CLI half, mounted on the `recall` parent command recall.go
// builds.
//
// Inputs: cobra args/flags; the same recallDeps recall.go's `recall`
// command already injects (this file adds no new dependency shape).
// Outputs: process output via internal/output only; a typed taxonomy
// error on failure.
// Constraints: one-shot and non-interactive, same as `cascade recall`
// itself; every verb routes through the D/S-07.T3 Go client SDK
// (recallCall), never a hand-rolled JSON-RPC request.
//
// SPORT: cmd.cascade.cmd.recall.index (ADD, P1-E06-W2-S11-T4).
package main

import (
	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/retrieval/lifecycle"
)

// newRecallIndexCmd builds `cascade recall index` and its four verbs.
func newRecallIndexCmd(deps recallDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "index",
		Short: "Manage the retrieval index lifecycle",
		Long: "Rebuild, verify, migrate, or incrementally update the retrieval\n" +
			"index. `rebuild` is the explicit full re-index repair path; " +
			"`update`\nre-ingests only the files a git diff reports changed since the\n" +
			"index's last recorded generation.",
	}
	cmd.AddCommand(
		newRecallIndexRebuildCmd(deps),
		newRecallIndexVerifyCmd(deps),
		newRecallIndexMigrateCmd(deps),
		newRecallIndexUpdateCmd(deps),
	)
	return cmd
}

// newRecallIndexRebuildCmd builds `cascade recall index rebuild`.
func newRecallIndexRebuildCmd(deps recallDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "rebuild",
		Short: "Fully re-index every registered source from a clean slate",
		Long: "Re-run the whole ingest/chunk/index/embed pipeline over every\n" +
			"registered source. A second rebuild over unchanged sources is a\n" +
			"no-op: it reports zero chunks written and zero deleted, exit 0.\n\n" +
			"This is the explicit repair path for a corrupted or drifted index\n" +
			"(see `recall index verify`); it is never run implicitly.",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			var result lifecycle.RebuildResult
			if err := recallCall(cmd, deps, daemon.RecallIndexRebuildMethod, nil, &result); err != nil {
				return err
			}
			return recallWriter(cmd).Result(result)
		},
	}
}

// newRecallIndexVerifyCmd builds `cascade recall index verify`.
func newRecallIndexVerifyCmd(deps recallDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "Check the index against its registered sources",
		Long: "Report missing, orphaned, and vector-incomplete chunks, plus\n" +
			"whether the index's recorded generation marker matches the\n" +
			"current working tree.",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			var result lifecycle.VerifyReport
			if err := recallCall(cmd, deps, daemon.RecallIndexVerifyMethod, nil, &result); err != nil {
				return err
			}
			return recallWriter(cmd).Result(result)
		},
	}
}

// newRecallIndexMigrateCmd builds `cascade recall index migrate`.
func newRecallIndexMigrateCmd(deps recallDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Run the retrieval index domain's schema migrations",
		Long: "Converge the retrieval index domain's on-disk schema to this\n" +
			"binary's version. Idempotent: a second run reports nothing to do.\n" +
			"Refuses an on-disk schema newer than this binary understands.",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			var result lifecycle.MigrateResult
			if err := recallCall(cmd, deps, daemon.RecallIndexMigrateMethod, nil, &result); err != nil {
				return err
			}
			return recallWriter(cmd).Result(result)
		},
	}
}

// newRecallIndexUpdateCmd builds `cascade recall index update`.
func newRecallIndexUpdateCmd(deps recallDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Incrementally re-index files changed since the last run",
		Long: "Re-ingest only the files a git diff reports changed since the\n" +
			"index's last recorded generation marker. Requires a prior\n" +
			"`recall index rebuild` to have recorded that marker.",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			var result lifecycle.UpdateResult
			if err := recallCall(cmd, deps, daemon.RecallIndexUpdateMethod, nil, &result); err != nil {
				return err
			}
			return recallWriter(cmd).Result(result)
		},
	}
}
