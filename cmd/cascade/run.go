// Purpose: `cascade run` (07-CLI-COMMAND-TREE note 9: "run is the only
// model door"): the sole command that dispatches a model job to the
// daemon's conductor.execute endpoint, through internal/client.Client only
// (cmd-rpc-server-boundary depguard rule), rendering via internal/output.
// Inputs: --task, --require, --sensitivity, --fan-out, --stream, --dry-run,
// --input and the hidden --resume, plus an injected runDeps.
// Outputs: the human view or the --json envelope, or a taxonomy error.
// Constraints: --task is checked against the frozen 9-class §5.16 set
// before any RPC call. --fan-out (hidden) is bounded 2..runMaxFanOut; a
// fan-out call sends a minted request id (printed to stderr) or the hidden
// --resume id, and refuses a response that does not echo it.
// SPORT: cmd/cascade/run (CHANGE, P1-CORE-19).
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// runTaskClasses is the frozen §5.16 nine-row task-class set, duplicated
// from internal/conductor/task_classes.go (the thin-client boundary bars
// the import) and pinned by TestRunCmd_TaskValidation.
var runTaskClasses = []string{
	"code", "reason", "review", "arbitrate", "classify",
	"segment", "summarize", "extract", "chat",
}

// runSensitivityNames lists the §5.16 tier names in member order, read from
// provider.SensitivityTier itself so the CLI keeps no second name table.
func runSensitivityNames() []string {
	var names []string
	for t := provider.SensitivityRestricted; t.Valid(); t++ {
		names = append(names, t.String())
	}
	return names
}

// runDialTimeout bounds the blocking conductor.execute round trip.
const runDialTimeout = 30 * time.Second

// runDeps carries every external input `cascade run` needs (statusDeps's
// injection pattern), so no test touches the real environment or socket.
type runDeps struct {
	Paths       runtime.PathProvider
	Getenv      runtime.Getenv
	Environ     func() []string
	DialContext func(ctx context.Context, socketPath string) (net.Conn, error)
}

// productionRunDeps builds runDeps against the real environment.
func productionRunDeps() runDeps {
	return runDeps{
		Paths:       lazyPaths{},
		Getenv:      os.Getenv,
		Environ:     os.Environ,
		DialContext: client.UnixDialer,
	}
}

// runFlags holds the parsed flag values for one invocation.
type runFlags struct {
	Task        string
	Require     map[string]string
	Sensitivity string
	FanOut      int
	Resume      string
	Stream      bool
	DryRun      bool
	Input       []string
}

// mountRunCmd attaches `run` to root (golden_help.txt carries it).
func mountRunCmd(root *cobra.Command) {
	root.AddCommand(newRunCmd(productionRunDeps()))
}

// newRunCmd builds the `run` command.
func newRunCmd(deps runDeps) *cobra.Command {
	var flags runFlags
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Dispatch a one-shot or streaming model task through the conductor",
		Long: "Dispatch a one-shot or streaming model task through the conductor.\n\n" +
			"When the resolved lane is the in-process local-model lane " +
			"(providers/agents/local), task classes classify, extract and " +
			"summarize always dispatch. Task classes code, reason, review and " +
			"arbitrate dispatch only for a model id that has passed the named " +
			"qualification fixture under providers/ollama/testdata/qualification/ " +
			"— authoring is a per-model capability, never a global flag, and no " +
			"config setting or environment variable can grant it. A model with no " +
			"recorded qualification, a failing one, or a stale one all refuse with " +
			"a not-qualified error.",
		Example: "  # Run the named qualification fixture against a model id to grant\n" +
			"  # it authoring task classes, then verify the result before use:\n" +
			"  cascade run --task classify --input \"triage this\"\n" +
			"  cascade run --task code --input \"...\"  # refuses until the model qualifies",
		Args: usageArgs(cobra.NoArgs),
		PreRunE: func(*cobra.Command, []string) error {
			if err := validateTaskClass(flags.Task); err != nil {
				return err
			}
			return validateFanOutFlags(flags)
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flags.FanOut >= 2 {
				return runFanOutCmd(cmd, deps, flags)
			}
			return runRunCmd(cmd, deps, flags)
		},
	}
	f := cmd.Flags()
	f.StringVar(&flags.Task, "task", "", "task class: "+strings.Join(runTaskClasses, "|")+" (required)")
	f.StringToStringVar(&flags.Require, "require", nil, "repeatable k=v requirement (reasoning|context|structured)")
	f.StringVar(&flags.Sensitivity, "sensitivity", "", "sensitivity tier: "+strings.Join(runSensitivityNames(), "|")+" (default restricted)")
	f.IntVar(&flags.FanOut, "fan-out", 0, "dispatch N parallel legs")
	f.BoolVar(&flags.Stream, "stream", false, "stream the response over GET /events")
	f.BoolVar(&flags.DryRun, "dry-run", false, "log the request without dispatching to any provider")
	f.StringArrayVar(&flags.Input, "input", nil, "repeatable chat turn, \"role:content\" (role defaults to user); reads stdin if omitted")
	f.StringVar(&flags.Resume, "resume", "", "re-attach to the fan-out with this request id")
	_ = f.MarkHidden("fan-out")
	_ = f.MarkHidden("resume")
	return cmd
}

// validateTaskClass fails early with a formatted error listing every
// valid class name, before any RPC call is ever made.
func validateTaskClass(task string) error {
	if task == "" {
		return cascade.Newf(cascade.KindInvalidInput, "cascade run: --task is required (valid: %s)", strings.Join(runTaskClasses, ", "))
	}
	for _, c := range runTaskClasses {
		if task == c {
			return nil
		}
	}
	return cascade.Newf(cascade.KindInvalidInput, "cascade run: --task %q is not a valid task class (valid: %s)", task, strings.Join(runTaskClasses, ", "))
}

// validateSensitivity checks --sensitivity with provider.ParseSensitivityTier,
// the one closed parser, and refuses on its error. It keeps no default of
// its own: the parser alone decides that "" is restricted and that any
// other unrecognised name is refused.
func validateSensitivity(tier string) error {
	if _, err := provider.ParseSensitivityTier(tier); err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, err, "cascade run: --sensitivity %q is not a valid tier (valid: %s)", tier, strings.Join(runSensitivityNames(), ", "))
	}
	return nil
}

// runRequestParams is the conductor.execute JSON-RPC params wire shape.
// dry_run is top-level (pkg/provider.Policy has no DryRun field to nest
// it under, a recorded contract deviation). Sensitivity travels as its
// tier NAME, so omitting it sends an empty string. request_id is set only
// on a fan-out call (runFanOutCmd).
type runRequestParams struct {
	TaskID       string           `json:"task_id"`
	TaskClass    string           `json:"task_class"`
	Inputs       []runChatMessage `json:"inputs"`
	Requirements runRequirements  `json:"requirements"`
	Sensitivity  string           `json:"sensitivity"`
	FanOut       int              `json:"fan_out,omitempty"`
	RequestID    string           `json:"request_id,omitempty"`
	DryRun       bool             `json:"dry_run,omitempty"`
}

// runRequirements is --require's k=v shape. The three closed LANE keys
// (reasoning, context, structured) map onto pkg/provider.Requirements, so
// an unknown lane key fails client-side. NodeCapabilities is the open NODE
// namespace behind the reserved `node.` prefix (`--require
// node.browser=true`): machines advertise those, so they are not a fixed
// enumeration the CLI can check.
type runRequirements struct {
	Reasoning  string `json:"reasoning,omitempty"`
	Context    int    `json:"context,omitempty"`
	Structured bool   `json:"structured,omitempty"`
	// NodeCapabilities are the capability names a node must report to be
	// eligible to run this work (S-37.T1 placement).
	NodeCapabilities []string `json:"node_capabilities,omitempty"`
}

type runChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// buildInputs parses --input entries ("role:content", role defaulting to
// "user") or, when none were given, reads all of stdin as one user turn.
func buildInputs(inputs []string, stdin io.Reader) ([]runChatMessage, error) {
	if len(inputs) == 0 {
		body, err := io.ReadAll(stdin)
		if err != nil {
			return nil, cascade.Wrap(cascade.KindInvalidInput, err, "cascade run: read stdin")
		}
		if len(strings.TrimSpace(string(body))) == 0 {
			return nil, cascade.New(cascade.KindInvalidInput, "cascade run: no --input given and stdin is empty")
		}
		return []runChatMessage{{Role: "user", Content: string(body)}}, nil
	}
	out := make([]runChatMessage, 0, len(inputs))
	for _, raw := range inputs {
		role, content, ok := strings.Cut(raw, ":")
		if !ok {
			role, content = "user", raw
		}
		out = append(out, runChatMessage{Role: role, Content: content})
	}
	return out, nil
}

// buildRunParams assembles the full wire params from flags and stdin.
func buildRunParams(flags runFlags, stdin io.Reader) (runRequestParams, error) {
	if err := validateSensitivity(flags.Sensitivity); err != nil {
		return runRequestParams{}, err
	}
	req, err := buildRequirements(flags.Require)
	if err != nil {
		return runRequestParams{}, err
	}
	inputs, err := buildInputs(flags.Input, stdin)
	if err != nil {
		return runRequestParams{}, err
	}
	return runRequestParams{
		TaskID: "cli-" + flags.Task, TaskClass: flags.Task, Inputs: inputs,
		Requirements: req, Sensitivity: flags.Sensitivity,
		FanOut: flags.FanOut, DryRun: flags.DryRun,
	}, nil
}

// runMaxFanOut mirrors conductor.MaxFanOut (the import is barred here).
const runMaxFanOut = 16

// validateFanOutFlags applies the daemon's fan-out bound client-side and
// checks the hidden --resume id.
func validateFanOutFlags(flags runFlags) error {
	if flags.FanOut > runMaxFanOut {
		return cascade.Newf(cascade.KindInvalidInput, "cascade run: --fan-out %d exceeds the maximum of %d", flags.FanOut, runMaxFanOut)
	}
	if flags.Resume == "" {
		return nil
	}
	if flags.FanOut < 2 {
		return cascade.New(cascade.KindInvalidInput, "cascade run: --resume needs --fan-out >= 2")
	}
	_, err := cascade.ParseID(flags.Resume)
	return err
}

// fanOutRunResponse is the fan-out parent response plus the echoed id.
type fanOutRunResponse struct {
	provider.ModelResponse
	RequestID string `json:"request_id"`
}

// runFanOutCmd sends a fan-out call with a request id (the --resume id, or
// one minted here and printed to stderr before dispatch) and refuses a
// response that does not echo it: such a daemon cannot re-attach.
func runFanOutCmd(cmd *cobra.Command, deps runDeps, flags runFlags) error {
	if flags.Stream {
		return cascade.New(cascade.KindInvalidInput, "cascade run: --stream is not supported with --fan-out")
	}
	params, err := buildRunParams(flags, cmd.InOrStdin())
	if err != nil {
		return err
	}
	if params.RequestID = flags.Resume; params.RequestID == "" {
		id, err := cascade.NewID()
		if err != nil {
			return err
		}
		params.RequestID = string(id)
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "cascade: request id %s\n", id)
	}
	settings, err := resolveRunSocket(cmd.Context(), deps)
	if err != nil {
		return err
	}
	var resp fanOutRunResponse
	c := client.New(settings.SocketPath, client.DialFunc(deps.DialContext), runDialTimeout)
	if err := c.Do(cmd.Context(), "conductor.execute", params, &resp); err != nil {
		return err
	}
	if resp.RequestID != params.RequestID {
		return cascade.New(cascade.KindUnsupported, "cascade run: daemon does not support re-attach (request id not echoed)")
	}
	return renderRunResult(runOutputWriter(cmd, deps), resp.ModelResponse)
}
