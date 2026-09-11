// Purpose: `cascade run` (07-CLI-COMMAND-TREE line 63-64, note 9: "run is
//
//	the only model door"): the sole cobra command that dispatches a
//	one-shot or streaming model.execute job to the daemon's
//	conductor.execute JSON-RPC endpoint. This file is the CLI-side half
//	only: it dials the daemon exclusively through internal/client.Client
//	(hard requirement 1, .golangci.yml's cmd-rpc-server-boundary depguard
//	rule) and renders through internal/output - never a hand-rolled
//	request of its own, never a second model-dispatch surface anywhere
//	else in cmd/cascade.
//
// Inputs: cobra args/flags (--task, --require, --sensitivity, --fan-out,
//
//	--stream, --dry-run, --input) plus a runDeps injected at construction
//	so no test touches the real environment or a real socket.
//
// Outputs: process output via internal/output.Writer - a human view by
//
//	default, or the versioned --json envelope forced on in non-interactive
//	mode (CASCADE_NO_INPUT=1 or a non-TTY stdout, §5.8 automation parity) -
//	and a taxonomy error on failure.
//
// Constraints: --task is validated client-side against the frozen 9-class
//
//	§5.16 set before any RPC call. --dry-run sets the wire params'
//	top-level dry_run field. --stream opens the real GET
//	/events?filter=job:<id> endpoint (P1-E11-W3-S23-T3 landed the
//	server side in the same phase this command ships in, so no
//	CASCADE-ALLOW placeholder is needed here - see this ticket's journal
//	for both sides quoted on the T1/T3 sequencing contract). --fan-out
//	stays MarkHidden: the fan-out primitive (K/S-23.T2) is real, but no
//	ticket in this file's scope unhides the CLI surface for it.
//
// SPORT: cmd/cascade/run (ADD, P1-E11-W3-S23-T1/T3).
package main

import (
	"context"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// runTaskClasses is the frozen §5.16 nine-row task-class set. cmd/cascade
// never imports internal/conductor (the thin-client boundary), so this
// list is its own, deliberately duplicated copy - identical in content to
// internal/conductor/task_classes.go's TaskClass consts, verified by
// TestRunCmd_TaskValidation against the same nine literal names.
var runTaskClasses = []string{
	"code", "reason", "review", "arbitrate", "classify",
	"segment", "summarize", "extract", "chat",
}

// runSensitivityTiers is the §5.16 sensitivity enum's four valid tier
// names, plus the empty string (omitted -> daemon-side fail-closed
// resolution to "restricted").
var runSensitivityTiers = []string{"local-only", "restricted", "internal", "public"}

// runDialTimeout bounds the blocking conductor.execute round trip.
// Streaming has no such bound (a subscription is long-lived by design).
const runDialTimeout = 30 * time.Second

// runDeps carries every external input `cascade run` needs, mirroring
// statusDeps's established injection pattern (status.go) so no test
// touches the real environment or a real socket.
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
	Stream      bool
	DryRun      bool
	Input       []string
}

// mountRunCmd attaches `run` to root. NOT YET CALLED from
// mountSubcommands (root.go/testdata/golden_help.txt are outside this
// ticket's files_scope and are concurrently owned by other work this
// phase) - see the journal for the exact one-line follow-up this leaves.
func mountRunCmd(root *cobra.Command) {
	root.AddCommand(newRunCmd(productionRunDeps()))
}

// newRunCmd builds the `run` command.
func newRunCmd(deps runDeps) *cobra.Command {
	var flags runFlags
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Dispatch a one-shot or streaming model task through the conductor",
		Args:  usageArgs(cobra.NoArgs),
		PreRunE: func(*cobra.Command, []string) error {
			return validateTaskClass(flags.Task)
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRunCmd(cmd, deps, flags)
		},
	}
	f := cmd.Flags()
	f.StringVar(&flags.Task, "task", "", "task class: "+strings.Join(runTaskClasses, "|")+" (required)")
	f.StringToStringVar(&flags.Require, "require", nil, "repeatable k=v requirement (reasoning|context|structured)")
	f.StringVar(&flags.Sensitivity, "sensitivity", "", "sensitivity tier: "+strings.Join(runSensitivityTiers, "|"))
	f.IntVar(&flags.FanOut, "fan-out", 0, "dispatch N parallel legs")
	f.BoolVar(&flags.Stream, "stream", false, "stream the response over GET /events")
	f.BoolVar(&flags.DryRun, "dry-run", false, "log the request without dispatching to any provider")
	f.StringArrayVar(&flags.Input, "input", nil, "repeatable chat turn, \"role:content\" (role defaults to user); reads stdin if omitted")
	// --fan-out stays hidden: K/S-23.T2's fan-out primitive is real, but
	// no ticket in this file's files_scope unhides this CLI surface for
	// it - see this file's header comment.
	_ = f.MarkHidden("fan-out")
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

// validateSensitivity fails closed on an unrecognized non-empty tier
// name; the empty string is always valid (daemon-side fail-closed
// resolution per §5.16).
func validateSensitivity(tier string) error {
	if tier == "" {
		return nil
	}
	for _, t := range runSensitivityTiers {
		if tier == t {
			return nil
		}
	}
	return cascade.Newf(cascade.KindInvalidInput, "cascade run: --sensitivity %q is not a valid tier (valid: %s)", tier, strings.Join(runSensitivityTiers, ", "))
}

// runRequestParams is the conductor.execute JSON-RPC params wire shape.
// CONTRACT DEVIATION (recorded, not papered over): the ticket text
// describes dry_run as "ModelRequest.policy.dry_run", but the real
// pkg/provider.Policy struct (frozen, out of files_scope) carries only
// ExternalAllowed - no DryRun field exists to nest it under. dry_run is
// therefore a top-level params field here, matching this struct's own
// literal wire shape rather than a field that does not exist on the SDK
// type. Sensitivity is sent as its tier NAME (a string), never
// provider.SensitivityTier's raw numeric encoding, so "omitting it sends
// an empty string" (the ticket's own wording) is literally true on the
// wire.
type runRequestParams struct {
	TaskID       string           `json:"task_id"`
	TaskClass    string           `json:"task_class"`
	Inputs       []runChatMessage `json:"inputs"`
	Requirements runRequirements  `json:"requirements"`
	Sensitivity  string           `json:"sensitivity"`
	FanOut       int              `json:"fan_out,omitempty"`
	DryRun       bool             `json:"dry_run,omitempty"`
}

// runRequirements is --require's k=v shape, mapped onto the three
// recognized keys (reasoning, context, structured) - pkg/provider.
// Requirements is a fixed three-field struct, not a generic map, so an
// unrecognized --require key is a client-side validation error rather
// than a silently-dropped or wire-passthrough value.
type runRequirements struct {
	Reasoning  string `json:"reasoning,omitempty"`
	Context    int    `json:"context,omitempty"`
	Structured bool   `json:"structured,omitempty"`
}

type runChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// buildRequirements maps --require's k=v pairs onto runRequirements.
func buildRequirements(kv map[string]string) (runRequirements, error) {
	var r runRequirements
	for k, v := range kv {
		var err error
		switch k {
		case "reasoning":
			r.Reasoning = v
		case "context":
			r.Context, err = strconv.Atoi(v)
		case "structured":
			r.Structured, err = strconv.ParseBool(v)
		default:
			return r, cascade.Newf(cascade.KindInvalidInput, "cascade run: --require key %q is not valid (valid: reasoning, context, structured)", k)
		}
		if err != nil {
			return r, cascade.Wrapf(cascade.KindInvalidInput, err, "cascade run: --require %s=%s", k, v)
		}
	}
	return r, nil
}

// buildInputs parses --input entries ("role:content", role defaulting to
// "user") or, when none were given, reads all of stdin as a single user
// turn - 06-FORGE-SPEC.md names no message-content flag for run at all
// (a contract gap; see the journal), and validateRequest's own non-empty-
// Inputs requirement makes SOME such mechanism unavoidable.
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
