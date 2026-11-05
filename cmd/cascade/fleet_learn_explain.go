// Purpose: `cascade fleet learn explain <scope_key> <task_class> [--json]`
//
//	(P1-LRN-04). Dials learn.explain and prints the posterior rows, the
//	tier-0 and best-lower estimates, the jump threshold and whether the
//	jump rule fires.
//
// Inputs: two positional arguments; the scope key is validated locally
// with internal/learn.ParseScopeKey before any dial.
//
// Outputs: a multi-line human summary; the versioned envelope in --json
// mode.
//
// Constraints: never prompts; output is identical under CASCADE_NO_INPUT=1.
//
// SPORT: cmd/cascade/fleet-learn-cli (ADD, P1-LRN-04).
package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/learn"
)

// learnExplainParams is the learn.explain request body.
type learnExplainParams struct {
	ScopeKey  string `json:"scope_key"`
	TaskClass string `json:"task_class"`
}

// learnExplainEstimate mirrors one capacity estimate in the explain reply.
type learnExplainEstimate struct {
	Tier            string        `json:"tier"`
	QueueWait       time.Duration `json:"queue_wait"`
	DurationEst     time.Duration `json:"duration_est"`
	PRework         float64       `json:"p_rework"`
	ReworkCyclesEst int           `json:"rework_cycles_est"`
	ExpectedTime    time.Duration `json:"expected_time"`
}

// learnExplainResult mirrors the learn.explain reply.
type learnExplainResult struct {
	Scores    []learnScoreRow       `json:"scores"`
	Tier0     *learnExplainEstimate `json:"tier0,omitempty"`
	BestLower *learnExplainEstimate `json:"best_lower,omitempty"`
	Threshold float64               `json:"threshold"`
	JumpFires bool                  `json:"jump_fires"`
}

// String renders the explanation as a short multi-line summary.
func (r learnExplainResult) String() string {
	var b strings.Builder
	if len(r.Scores) == 0 {
		b.WriteString("scores: none\n")
	}
	for _, s := range r.Scores {
		fmt.Fprintf(&b, "score tier=%s score=%.3f alpha=%.2f beta=%.2f observations=%d fallback=%s\n",
			s.Tier, s.Score, s.Alpha, s.Beta, s.Observations, s.FallbackLevel)
	}
	writeExplainEstimate(&b, "tier0", r.Tier0)
	writeExplainEstimate(&b, "best_lower", r.BestLower)
	fmt.Fprintf(&b, "threshold=%.3f jump_fires=%t", r.Threshold, r.JumpFires)
	return b.String()
}

// writeExplainEstimate appends one estimate line, or "<label>: none" when
// the reply carried no estimate.
func writeExplainEstimate(b *strings.Builder, label string, e *learnExplainEstimate) {
	if e == nil {
		fmt.Fprintf(b, "%s: none\n", label)
		return
	}
	fmt.Fprintf(b, "%s tier=%s queue_wait=%s duration_est=%s p_rework=%.3f rework_cycles_est=%d expected_time=%s\n",
		label, e.Tier, e.QueueWait, e.DurationEst, e.PRework, e.ReworkCyclesEst, e.ExpectedTime)
}

// newFleetLearnExplainCmd builds `fleet learn explain <scope_key> <task_class>`.
func newFleetLearnExplainCmd(deps fleetSessionsDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "explain <scope_key> <task_class>",
		Short: "Explain the learned estimate for one scope and task class",
		Long: "Explain how the scorer ranks tiers for one scope_key (repo:<id>,\n" +
			"lang:<Language> or global) and task_class: the posterior rows, the\n" +
			"tier-0 and best-lower estimates, the jump threshold and whether the\n" +
			"jump rule fires. Requires a running daemon (`cascade daemon run`).\n" +
			"Output is identical with CASCADE_NO_INPUT=1: this command never prompts.",
		Example: "  cascade fleet learn explain global code\n" +
			"  cascade fleet learn explain repo:abc123 review --json",
		Args: usageArgs(cobra.ExactArgs(2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := learn.ParseScopeKey(args[0]); err != nil {
				return err
			}
			c, err := dialFleetLearnClient(cmd.Context(), deps)
			if err != nil {
				return err
			}
			var out learnExplainResult
			params := learnExplainParams{ScopeKey: args[0], TaskClass: args[1]}
			if err := c.Do(cmd.Context(), learnExplainMethod, params, &out); err != nil {
				return err
			}
			return fleetSessionsOutputWriter(cmd).Result(out)
		},
	}
}
