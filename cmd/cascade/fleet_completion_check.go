// Purpose: the hidden `cascade fleet completion-check --event <evt> --socket
//
//	<path>` subcommand: the whole body of the completion-gate hook. The
//	harness runs it for TaskCompleted and Stop; it reads the harness's hook
//	JSON from stdin once (only stop_hook_active, from the decoded object),
//	builds the completion check params with encoding/json from
//	CASCADE_SESSION_ID, CASCADE_JOB_ID and CASCADE_TICKET_ID, makes ONE call
//	over the daemon socket with the Go client SDK under a deadline of the
//	server's completion timeout plus five seconds, and decodes the reply into
//	a typed struct. Nothing is interpolated into a shell string and nothing is
//	matched by substring.
//
// Inputs: the two flags, stdin, the three environment ids (through
//
//	fleetSessionsDeps.Getenv), a dial function (fleetSessionsDeps).
//
// Outputs: exit 0 only for a decoded `deny: false`. Every other outcome is
//
//	an error whose message is one line and whose kind is invalid-input, which
//	is exit 2: the harness reads exit 2 as "block, and show stderr", and this
//	command's exit contract is the harness's, not the general taxonomy's, so
//	the cause rides in the message rather than the exit code. The rendered
//	command's trailing `|| exit 2` covers the ways this process can end
//	without reaching that code (see hookpacks.completionHookCommand).
//
// Constraints: fail closed. A missing or malformed reply, a reply without an
//
//	explicit deny, a transport failure, a daemon error and a timeout all
//	refuse. The command skips the root pre-run (no daemon probe, no profile
//	attach): it must answer in its own budget and make exactly one request.
//
// SPORT: cmd/cascade/fleet-completion-check (ADD, P1-CI-09).
package main

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	// completionCheckMaxStdin bounds the harness JSON read from stdin.
	completionCheckMaxStdin = 1 << 20
	// completionCheckMaxReason bounds the denial reason echoed to stderr.
	completionCheckMaxReason = 2048
	// completionCheckDeniedFallback is the reason when a denial carries none.
	completionCheckDeniedFallback = "completion check denied"
)

// completionCheckReply is the typed result of the completion check. Deny is
// a pointer so a reply that omits it, or sends null, is distinguishable from
// an explicit false: only an explicit false allows.
type completionCheckReply struct {
	Deny   *bool  `json:"deny"`
	Reason string `json:"reason"`
}

// completionCheckStdin is the one field of the harness hook JSON this command
// reads; unknown fields are ignored because the harness adds them over time.
type completionCheckStdin struct {
	StopHookActive bool `json:"stop_hook_active"`
}

// newFleetCompletionCheckCmd builds the hidden `completion-check` command.
func newFleetCompletionCheckCmd(deps fleetSessionsDeps) *cobra.Command {
	var event, socket string
	cmd := &cobra.Command{
		Use:    "completion-check --event <TaskCompleted|Stop> --socket <path>",
		Short:  "Ask the daemon whether a completion may proceed (installed by the completion-gate hook pack)",
		Hidden: true,
		Args:   usageArgs(cobra.NoArgs),
		// Skip the root pre-run: no daemon probe, no profile attach.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		SilenceUsage:      true,
		SilenceErrors:     true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCompletionCheck(cmd, deps, event, socket)
		},
	}
	// A bad flag is the same exit-2 refusal wherever the command is mounted.
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return completionRefusal("request failed: invalid flag: " + err.Error())
	})
	cmd.Flags().StringVar(&event, "event", "", "harness event: TaskCompleted or Stop")
	cmd.Flags().StringVar(&socket, "socket", "", "daemon socket path")
	return cmd
}

// mountFleetCompletionCheck attaches the hidden command to the `fleet` noun
// mountFleetCmd has just registered on root. It never creates a root noun.
func mountFleetCompletionCheck(root *cobra.Command) {
	for _, c := range root.Commands() {
		if c.Name() == "fleet" {
			c.AddCommand(newFleetCompletionCheckCmd(productionFleetSessionsDeps()))
			return
		}
	}
}

// runCompletionCheck is the whole command: nil only for an explicit allow.
func runCompletionCheck(cmd *cobra.Command, deps fleetSessionsDeps, event, socket string) error {
	evt, err := completionCheckEvent(event)
	if err != nil {
		return err
	}
	if socket == "" {
		return completionRefusal("request failed: no daemon socket given")
	}
	budget := hookpacks.CompletionClientBudget()
	ctx, cancel := context.WithTimeout(cmd.Context(), budget)
	defer cancel()

	payload := hookpacks.CompletionHookPayload{
		EventType:      evt,
		SessionID:      deps.Getenv("CASCADE_SESSION_ID"),
		JobID:          deps.Getenv("CASCADE_JOB_ID"),
		TaskID:         deps.Getenv("CASCADE_TICKET_ID"),
		StopHookActive: readStopHookActive(ctx, cmd.InOrStdin()),
	}
	if ctx.Err() != nil {
		return completionRefusal("request failed (timeout)")
	}
	var reply completionCheckReply
	c := client.New(socket, client.DialFunc(deps.DialContext), budget)
	if err := c.Do(ctx, hookpacks.MethodCompletionCheck, payload, &reply); err != nil {
		return completionRefusal("request failed (" + completionErrorKind(err) + ")")
	}
	return completionVerdict(reply)
}

// completionCheckEvent accepts only the two events the pack installs.
func completionCheckEvent(event string) (hookpacks.HookEventType, error) {
	evt := hookpacks.HookEventType(event)
	if evt == hookpacks.EventTaskCompleted || evt == hookpacks.EventStop {
		return evt, nil
	}
	return "", completionRefusal("request failed: --event must be TaskCompleted or Stop")
}

// completionVerdict maps the decoded reply to the command's result: nil for
// an explicit deny=false, a one-line refusal for everything else.
func completionVerdict(reply completionCheckReply) error {
	switch {
	case reply.Deny == nil:
		return completionRefusal("response malformed")
	case !*reply.Deny:
		return nil
	case strings.TrimSpace(reply.Reason) == "":
		return completionRefusal(completionCheckDeniedFallback)
	}
	return completionRefusal(reply.Reason)
}

// readStopHookActive decodes stop_hook_active from the harness JSON on r. An
// empty, oversized or non-object stdin reads as false: the flag changes no
// decision, so a stdin quirk must not turn a clean allow into a block. The
// read has a one-second sub-timeout so an open stdin leaves time for the
// daemon call. An earlier caller deadline still takes precedence.
func readStopHookActive(ctx context.Context, r io.Reader) bool {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	done := make(chan bool, 1)
	go func() {
		raw, err := io.ReadAll(io.LimitReader(r, completionCheckMaxStdin+1))
		var in completionCheckStdin
		if err != nil || len(raw) > completionCheckMaxStdin || json.Unmarshal(raw, &in) != nil {
			done <- false
			return
		}
		done <- in.StopHookActive
	}()
	select {
	case v := <-done:
		return v
	case <-ctx.Done():
		return false
	}
}

// completionErrorKind names err's taxonomy kind, or "internal".
func completionErrorKind(err error) string {
	if kind, ok := cascade.KindOf(err); ok {
		return kind.String()
	}
	return "internal"
}

// completionRefusal builds the command's one-line, exit-2 error. Control
// characters, including newlines, become spaces, and the text is capped, so
// a reason from the daemon can never add a second line to stderr.
func completionRefusal(reason string) error {
	line := strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, reason)
	if len(line) > completionCheckMaxReason {
		line = strings.ToValidUTF8(line[:completionCheckMaxReason], "") + "..."
	}
	return cascade.New(cascade.KindInvalidInput, line)
}
