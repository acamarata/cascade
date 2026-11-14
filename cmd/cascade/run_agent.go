// Purpose: `cascade run --agent <intent>`: the CLI client of the daemon's
// conductor.delegate and conductor.delegate_wait verbs. It sends the intent,
// then waits for the addressed result, through internal/client only.
// Inputs: --agent, --task-class, --min-quality, --deadline, --mutation, --job,
// --timeout, plus the injected runDeps.
// Outputs: the job id and terminal state (human view or the --json envelope),
// or a taxonomy error.
// Constraints: the daemon does all spawning, leasing and envelope work; with no
// daemon the command refuses (KindUnavailable) and never runs anything inline.
// With CASCADE_NO_INPUT=1 it never prompts: every input comes from a flag.
// SPORT: cmd/cascade/run (CHANGE).
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	runAgentDelegateMethod = "conductor.delegate"
	runAgentWaitMethod     = "conductor.delegate_wait"
	runAgentDefaultWait    = 5 * time.Minute
	runAgentWaitSlack      = 30 * time.Second
)

// errRunAgentNoDaemon is the refusal for a missing daemon.
const errRunAgentNoDaemon = "the daemon is not running: cascade run --agent needs it (start it with `cascade daemon start`)"

// agentFlags holds the --agent flag set.
type agentFlags struct {
	Intent     string
	TaskClass  string
	MinQuality string
	Deadline   string
	Mutation   bool
	JobID      string
	Timeout    time.Duration
}

// addRunAgentFlags registers the --agent flag set on cmd.
func addRunAgentFlags(cmd *cobra.Command, a *agentFlags) {
	f := cmd.Flags()
	f.StringVar(&a.Intent, "agent", "", "delegate this intent to an agent through the daemon")
	f.StringVar(&a.TaskClass, "task-class", "", "delegation task class (default reason, or code with --mutation)")
	f.StringVar(&a.MinQuality, "min-quality", "", "minimum quality: standard|high|max")
	f.StringVar(&a.Deadline, "deadline", "", "deadline class: interactive|batch")
	f.BoolVar(&a.Mutation, "mutation", false, "the delegation may change files")
	f.StringVar(&a.JobID, "job", "", "attach the delegation to this job id")
	f.DurationVar(&a.Timeout, "timeout", runAgentDefaultWait, "how long to wait for the result")
}

// agentDelegateParams is the conductor.delegate wire shape.
type agentDelegateParams struct {
	Intent     string `json:"intent"`
	TaskClass  string `json:"task_class,omitempty"`
	MinQuality string `json:"min_quality,omitempty"`
	Deadline   string `json:"deadline,omitempty"`
	Mutation   bool   `json:"mutation"`
	JobID      string `json:"job_id,omitempty"`
}

// agentDelegateReply is the conductor.delegate reply.
type agentDelegateReply struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

// agentWaitParams is the conductor.delegate_wait wire shape.
type agentWaitParams struct {
	JobID     string `json:"job_id"`
	TimeoutMS int64  `json:"timeout_ms"`
}

// agentResultView is the conductor.delegate_wait reply and the --json data.
type agentResultView struct {
	JobID     string   `json:"job_id"`
	State     string   `json:"state"`
	Output    string   `json:"output,omitempty"`
	Artifacts []string `json:"artifacts,omitempty"`
	DataClass string   `json:"data_class"`
}

// String renders the result as one operator line (job, state, output size).
func (v agentResultView) String() string {
	return fmt.Sprintf("job %s %s (%d bytes of output, %d artifacts)", v.JobID, v.State, len(v.Output), len(v.Artifacts))
}

// runAgentCmd delegates the intent, waits for the result and renders it.
func runAgentCmd(cmd *cobra.Command, deps runDeps, a agentFlags) error {
	if strings.TrimSpace(a.Intent) == "" || a.Timeout < 0 {
		return cascade.New(cascade.KindInvalidInput, "cascade run: --agent needs a non-empty intent and a non-negative --timeout")
	}
	ctx := cmd.Context()
	if st, ok := runtime.DaemonlessStateFrom(ctx); ok && st.Embedded {
		return cascade.New(cascade.KindUnavailable, errRunAgentNoDaemon)
	}
	settings, err := resolveRunSocket(ctx, deps)
	if err != nil {
		return err
	}
	dial := client.DialFunc(deps.DialContext)
	var sent agentDelegateReply
	err = client.New(settings.SocketPath, dial, runDialTimeout).Do(ctx, runAgentDelegateMethod, agentDelegateParams{
		Intent: a.Intent, TaskClass: a.TaskClass, MinQuality: a.MinQuality,
		Deadline: a.Deadline, Mutation: a.Mutation, JobID: a.JobID}, &sent)
	if err != nil {
		return agentDialError(err)
	}
	res, err := waitAgent(ctx, settings.SocketPath, dial, sent.JobID, a.Timeout)
	if err != nil {
		return err
	}
	return renderAgentResult(runOutputWriter(cmd, deps), res)
}

// agentDialError rewrites a transport refusal as the daemon-not-running
// error; a typed answer from a live daemon passes through unchanged.
func agentDialError(err error) error {
	if cascade.HasKind(err, cascade.KindUnavailable) && strings.Contains(err.Error(), "daemon not running or unreachable") {
		return cascade.Wrap(cascade.KindUnavailable, err, errRunAgentNoDaemon)
	}
	return err
}

// waitAgent calls conductor.delegate_wait; the HTTP timeout outlasts the wait.
func waitAgent(ctx context.Context, socket string, dial client.DialFunc, jobID string, wait time.Duration) (agentResultView, error) {
	var res agentResultView
	c := client.New(socket, dial, wait+runAgentWaitSlack)
	err := c.Do(ctx, runAgentWaitMethod, agentWaitParams{JobID: jobID, TimeoutMS: wait.Milliseconds()}, &res)
	return res, err
}

// renderAgentResult writes res through w: the --json envelope under
// non-interactive output, else the agent output and a one-line summary.
func renderAgentResult(w *output.Writer, res agentResultView) error {
	if w.Mode().JSON {
		return w.Result(res)
	}
	if res.Output != "" {
		w.Println(res.Output)
	}
	w.Warn("job_id=%s state=%s", res.JobID, res.State)
	return nil
}

// preflightRun validates the flags before any RPC: --agent mode needs only
// its own checks (made in runAgentCmd), the task path needs a valid class.
func preflightRun(flags runFlags) error {
	if flags.Agent.Intent != "" {
		return nil
	}
	if err := validateTaskClass(flags.Task); err != nil {
		return err
	}
	return validateFanOutFlags(flags)
}

// dispatchRun routes one `cascade run` invocation to its mode.
func dispatchRun(cmd *cobra.Command, deps runDeps, flags runFlags) error {
	if flags.Agent.Intent != "" {
		return runAgentCmd(cmd, deps, flags.Agent)
	}
	if flags.FanOut >= 2 {
		return runFanOutCmd(cmd, deps, flags)
	}
	return runRunCmd(cmd, deps, flags)
}
