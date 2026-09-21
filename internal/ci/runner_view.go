// Purpose (this file): `cascade ci run`'s Result view types
// (runResultView/stepResultView) -- split out of runner_cmd.go purely to
// keep that file under Art.10.3's 300-line cap (the same R-14.117 remedy
// config_widget.go/config_economics.go and internal/ci's own
// domain_source.go already apply for this ticket).
//
// Inputs: a RunResult (runner.go).
// Outputs: a JSON-taggable view for --json (output.Writer.Result), plus
// its human-mode String() rendering.
// Constraints: pure formatting, no I/O.
// SPORT: internal.ci.runResultView/ADDED (P1-E25-W5-S51-T5).

package ci

import (
	"fmt"
	"strings"
)

// runResultView is `ci run`'s Result payload: JSON-tagged for --json,
// String()-rendered for human mode.
type runResultView struct {
	RunID      int64            `json:"run_id"`
	RepoID     int64            `json:"repo_id"`
	Passed     bool             `json:"passed"`
	FailedStep StepKind         `json:"failed_step,omitempty"`
	Steps      []stepResultView `json:"steps"`
}

type stepResultView struct {
	Step     StepKind `json:"step"`
	Passed   bool     `json:"passed"`
	ExitCode int      `json:"exit_code"`
	TimedOut bool     `json:"timed_out,omitempty"`
}

func newRunResultView(r RunResult) runResultView {
	steps := make([]stepResultView, 0, len(r.Steps))
	for _, sr := range r.Steps {
		steps = append(steps, stepResultView{Step: sr.Step.Kind, Passed: sr.Passed(), ExitCode: sr.ExitCode, TimedOut: sr.TimedOut})
	}
	return runResultView{RunID: r.RunID, RepoID: r.RepoID, Passed: r.Passed(), FailedStep: r.FailedStep, Steps: steps}
}

// String renders one line per step plus a final PASSED/FAILED summary.
func (v runResultView) String() string {
	var buf strings.Builder
	for _, s := range v.Steps {
		status := "pass"
		if !s.Passed {
			status = "fail"
		}
		fmt.Fprintf(&buf, "%-6s %s (exit %d)\n", s.Step, status, s.ExitCode)
	}
	if v.Passed {
		buf.WriteString("PASSED")
	} else {
		fmt.Fprintf(&buf, "FAILED at %s", v.FailedStep)
	}
	return buf.String()
}
