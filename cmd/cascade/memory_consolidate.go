// Purpose: `cascade memory consolidate` — the manual trigger for the
//
//	background consolidation job, and its rendered view. Split from
//	memory.go and memory_view.go under Art.10.3's 300-line file cap; the
//	verb and the view it prints are kept together because what this
//	command must say about a destructive background job is one concern.
//
// Inputs: cobra flags; a memoryDeps injected at construction.
// Outputs: the ConsolidationReport, rendered for a terminal or as the
//
//	report's own JSON shape.
//
// Constraints: non-interactive (§5.8), so --dry-run is the rehearsal;
//
//	never writes to os.Stdout/os.Stderr directly; no platform-specific
//	imports (Art.5).
//
// SPORT: cmd.cascade.cmd.memory.consolidate (ADD, P1-E07-W2-S13-T4).
package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/memory"
)

// newMemoryConsolidateCmd builds `cascade memory consolidate`: the manual
// trigger for the background consolidation job.
func newMemoryConsolidateCmd(deps memoryDeps) *cobra.Command {
	var params memory.ConsolidateParams
	cmd := &cobra.Command{
		Use:   "consolidate",
		Short: "Retire exact-duplicate memory records into one survivor",
		Long: "Run the consolidation job once, now. It groups records whose\n" +
			"bodies are byte-identical within one kind, keeps the OLDEST of\n" +
			"each group, and tombstones the rest. Content is never rewritten:\n" +
			"the survivor's file is left exactly as it was.\n\n" +
			"Nothing is lost silently. Before any record is retired, a\n" +
			"consolidation record under the store's consolidations/ directory\n" +
			"captures every retired record in full — its address, description,\n" +
			"provenance and the body they shared — so a record you remember\n" +
			"writing can always be accounted for.\n\n" +
			"Nothing prompts. Use --dry-run to see exactly what would be\n" +
			"retired before letting it happen. The same job also runs on a\n" +
			"schedule inside the daemon; running it here is not a substitute.",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			var result memory.ConsolidationReport
			if err := memoryCall(cmd, deps, memory.MethodConsolidate, params, &result); err != nil {
				return err
			}
			return memoryWriter(cmd).Result(consolidateView{result})
		},
	}
	cmd.Flags().BoolVar(&params.DryRun, "dry-run",
		false, "report what would be retired without retiring it")
	return cmd
}

// consolidateView renders memory.consolidate's report. The embedding is
// anonymous so the --json payload is ConsolidationReport's own shape.
type consolidateView struct {
	memory.ConsolidationReport
}

// String says what the run did, and for a rehearsal what it would do.
//
// It names every group rather than printing a count alone. A job that
// retires a user's own records and reports only "merged 3" leaves them no
// way to tell WHICH three, and the whole discipline of this subsystem is
// that a removed memory stays accountable.
func (v consolidateView) String() string {
	var b strings.Builder
	b.WriteString(v.headline())
	for _, g := range v.Groups {
		fmt.Fprintf(&b, "\n  %s <- %s", g.CanonicalID, strings.Join(g.MemberIDs, ", "))
	}
	if len(v.Unreadable) > 0 {
		fmt.Fprintf(&b, "\n  unreadable, left untouched: %s", strings.Join(v.Unreadable, ", "))
	}
	if !v.DryRun && v.Retired > 0 {
		b.WriteString("\n  every retired record is recorded in the store's consolidations/ directory")
	}
	return b.String()
}

// headline is the one-line summary the rest of the view hangs off.
func (v consolidateView) headline() string {
	switch {
	case v.Skipped:
		return "a consolidation is already running; nothing was changed"
	case v.DryRun && len(v.Groups) == 0:
		return "dry run: nothing to consolidate"
	case v.DryRun:
		return fmt.Sprintf("dry run: would consolidate %d group(s), retiring %d record(s)",
			len(v.Groups), v.plannedRetirements())
	case v.NoChange:
		return "nothing to consolidate"
	default:
		return fmt.Sprintf("consolidated %d group(s), retiring %d record(s) by %s",
			v.Merged, v.Retired, v.Method)
	}
}

// plannedRetirements counts the records a dry run would retire, which the
// report's Retired field deliberately does not carry: nothing was retired.
func (v consolidateView) plannedRetirements() int {
	n := 0
	for _, g := range v.Groups {
		n += len(g.MemberIDs)
	}
	return n
}
