// Purpose (this file): `cascade ci status` -- split out of runner_cmd.go
// purely to keep that file under Art.10.3's 300-line cap (same R-14.117
// remedy as runner_view.go and domain_source.go).
//
// Inputs: CmdDeps (runner_cmd.go). NO flags: the contract says `cascade ci
// status` ships "exactly these base flags", and it names none for status --
// the earlier --limit was an invention. The page size is the documented
// statusRunLimit below, and a later ticket that genuinely needs paging
// declares the flag in its own contract.
// Outputs: process output via internal/output.Writer -- a combined view
// of both source=github-actions and source=local ci_run rows.
// Constraints: read-only; always exits zero (a report, never a gate).
// SPORT: internal.ci.newCIStatusCmd/ADDED (P1-E25-W5-S51-T5).

package ci

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// statusRunLimit is how many of the most recent runs `ci status` shows.
// A named constant rather than a flag, for the reason this file's Inputs
// note gives.
const statusRunLimit = 20

// newCIStatusCmd builds `cascade ci status`.
func newCIStatusCmd(deps CmdDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show recent CI results from both GitHub Actions and the local gate",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCIStatus(cmd, deps, statusRunLimit)
		},
	}
}

// runCIStatus implements `ci status`'s RunE.
//
// It is a READ of the ci_results domain and consults no policy: it polls
// nothing and spends nothing, so there is no paid direction here to guard.
// The never-pay refusals live where money is actually at stake -- `ci run`
// (run_cmd.go) and the Actions polling client (poll.go's guardActionsPoll).
func runCIStatus(cmd *cobra.Command, deps CmdDeps, limit int) error {
	ctx := cmd.Context()
	paths, err := deps.resolvePaths()
	if err != nil {
		return err
	}
	db, err := openResultsDB(ctx, paths.DataDir(), deps.Clock)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	runs, err := ListRuns(ctx, db, limit)
	if err != nil {
		return err
	}
	return outputWriter(cmd).Result(newStatusView(runs))
}

// statusView is `ci status`'s Result payload.
type statusView struct {
	Runs []statusRunView `json:"runs"`
}

type statusRunView struct {
	RunID      int64  `json:"run_id"`
	RepoID     int64  `json:"repo_id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Source     string `json:"source"`
}

func newStatusView(runs []RunSummary) statusView {
	out := make([]statusRunView, 0, len(runs))
	for _, r := range runs {
		out = append(out, statusRunView{
			RunID: r.RunID, RepoID: r.RepoID, Name: r.Name,
			Status: string(r.Status), Conclusion: string(r.Conclusion), Source: r.Source,
		})
	}
	return statusView{Runs: out}
}

// String renders one line per run: name, source, status/conclusion.
func (v statusView) String() string {
	if len(v.Runs) == 0 {
		return "(no CI results recorded)"
	}
	var buf strings.Builder
	for i, r := range v.Runs {
		if i > 0 {
			buf.WriteString("\n")
		}
		fmt.Fprintf(&buf, "%-24s %-14s %s/%s", r.Name, r.Source, r.Status, r.Conclusion)
	}
	return buf.String()
}
