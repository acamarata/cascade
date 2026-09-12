// Purpose: `cascade context sync --project-list <file>` (P1-E26-W10-S53-T3):
//
//	the bulk multi-project driver R-14.81 requires living on the existing
//	`context sync` surface rather than a new `migrate v1
//	regen-instructions` subcommand. Split from context_sync_cmd.go (not
//	named there despite this ticket's own files_scope) for the same
//	300-line-cap reason context_sync_cmd.go's own header gives for its
//	split from context_cmd.go — recorded in this ticket's journal.
//
// Inputs: the --project-list path and --check/--yes flag values from
//
//	newContextSyncCmd; contextScopeDeps for Getenv (CASCADE_NO_INPUT).
//
// Outputs: process output via internal/output.Writer (contextScopeOutputWriter);
//
//	a typed cascade.Error on failure. --check always exits 0 regardless of
//	drift (this ticket's full_desc, deliberately different from
//	single-project sync's --check, which fails on stale — see this
//	ticket's journal for the contract citation).
//
// Constraints: never a hand-rolled prompt loop shared with backup's own —
//
//	this is a distinct y/N reader over cmd.InOrStdin/ErrOrStderr because
//	backup's confirmBackupOperation returns a single error (refuse/allow),
//	not the three-way CheckOnly/Yes/NoInput answer internal/migration.Run
//	needs per project.
//
// SPORT: cli/context-sync-projects/ADD (P1-E26-W10-S53-T3 sport_updates).
package main

import (
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/migration"
	"github.com/acamarata/cascade/pkg/cascade"
)

// runContextSyncProjects is newContextSyncCmd's RunE branch for bulk mode.
func runContextSyncProjects(cmd *cobra.Command, deps contextScopeDeps, projectListPath string, checkOnly, yes bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "cascade context sync --project-list: resolve cwd")
	}
	list, err := migration.LoadProjectList(projectListPath, cwd)
	if err != nil {
		return err
	}
	noInput := deps.Getenv != nil && deps.Getenv("CASCADE_NO_INPUT") == "1"
	report, runErr := migration.Run(cmd.Context(), migration.RunOptions{
		// No v2 project registry exists yet to merge against (verified
		// against the tree — see this ticket's journal); the operator's
		// --project-list file is the entire set for this run.
		ProjectPaths: migration.MergeProjectPaths(list, nil),
		CheckOnly:    checkOnly,
		Yes:          yes,
		NoInput:      noInput,
		Confirm:      confirmProjectApply(cmd),
	})
	if err := contextScopeOutputWriter(cmd).Result(report); err != nil {
		return err
	}
	if checkOnly {
		// full_desc: "with --check the run is purely read-only and exits 0
		// regardless of drift" — deliberately not single-project sync's
		// --check contract (contextSyncOutcomeError), which fails on stale.
		return nil
	}
	return runErr
}

// confirmProjectApply builds the per-project y/N prompt bulk apply mode
// uses, reading cmd.InOrStdin() and writing cmd.ErrOrStderr() so tests can
// drive it without a real terminal, mirroring confirmBackupOperation's
// established stdin/stderr seam (backup_elevation.go) — a separate
// function because that one returns a single refuse/allow error, not the
// per-project bool internal/migration.RunOptions.Confirm needs.
func confirmProjectApply(cmd *cobra.Command) func(string) (bool, error) {
	return func(projectPath string) (bool, error) {
		prompt := "apply drift to " + projectPath + "? [y/N] "
		if _, err := cmd.ErrOrStderr().Write([]byte(prompt)); err != nil {
			return false, cascade.Wrap(cascade.KindUnavailable, err, "context sync: write confirmation prompt")
		}
		line := make([]byte, 16)
		n, _ := cmd.InOrStdin().Read(line)
		answer := strings.ToLower(strings.TrimSpace(string(line[:n])))
		return answer == "y" || answer == "yes", nil
	}
}
