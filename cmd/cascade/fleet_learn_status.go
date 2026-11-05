// Purpose: `cascade fleet learn status [--json]` (P1-LRN-04). Dials
//
//	learn.status and prints the decayed Beta posterior per (scope_key,
//	task_class, tier) row.
//
// Inputs: no positional arguments; --json is the root's persistent flag.
//
// Outputs: one line per score row in human mode; the versioned envelope
// in --json mode.
//
// Constraints: never prompts; output is identical under CASCADE_NO_INPUT=1.
//
// SPORT: cmd/cascade/fleet-learn-cli (ADD, P1-LRN-04).
package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// learnStatusResult mirrors the learn.status reply: {scores}.
type learnStatusResult struct {
	Scores []learnScoreRow `json:"scores"`
}

// String renders one line per score row, or a single empty-state line.
func (r learnStatusResult) String() string {
	if len(r.Scores) == 0 {
		return "no learned scores"
	}
	lines := make([]string, 0, len(r.Scores))
	for _, s := range r.Scores {
		lines = append(lines, fmt.Sprintf(
			"tier=%s task_class=%s score=%.3f alpha=%.2f beta=%.2f observations=%d fallback=%s",
			s.Tier, s.TaskClass, s.Score, s.Alpha, s.Beta, s.Observations, s.FallbackLevel))
	}
	return strings.Join(lines, "\n")
}

// newFleetLearnStatusCmd builds `fleet learn status`.
func newFleetLearnStatusCmd(deps fleetSessionsDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the learned capability scores",
		Long: "Show the decayed Beta capability score for each (scope_key,\n" +
			"task_class, tier) row the scorer has learned. Requires a running daemon\n" +
			"(`cascade daemon run`). Output is identical with CASCADE_NO_INPUT=1:\n" +
			"this command never prompts.",
		Example: "  cascade fleet learn status\n" +
			"  cascade fleet learn status --json",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := dialFleetLearnClient(cmd.Context(), deps)
			if err != nil {
				return err
			}
			var out learnStatusResult
			if err := c.Do(cmd.Context(), learnStatusMethod, struct{}{}, &out); err != nil {
				return err
			}
			return fleetSessionsOutputWriter(cmd).Result(out)
		},
	}
}
