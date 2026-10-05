// Purpose: `cascade fleet sessions hook-event <Event>` (hidden), the command
//
//	the sessions hook pack installs for SessionStart, PreToolUse,
//	PostToolUse, Stop and SessionEnd (contract:harness-session-registration).
//	It reads the harness's native hook JSON on stdin, takes the harness's
//	own session_id from it, and posts one fleet.sessions.hook_event to the
//	daemon so a live session is registered and tracked.
//
// Inputs: the event name as the only argument; the harness hook JSON on
//
//	stdin (read capped at 1 MiB; only session_id and hook_event_name are
//	decoded, every other field is ignored, so a newer client that adds
//	fields is still understood); the daemon socket resolved the way every
//	other fleet verb resolves it.
//
// Outputs: nothing on stdout, ever. On a failed post, exactly one stderr
//
//	line `cascade: fleet session hook not delivered: <kind>`. Input that
//	fails a check below sends nothing and prints nothing.
//
// Constraints: the sessions pack is non-gating. Every path returns nil, so
//
//	the process exits 0, and it never exits 2, so it can never block a
//	tool call. The command overrides the root's PersistentPreRunE with a
//	no-op: the root's daemonless probe and profile attachment can fail or
//	print, and neither belongs in a hook. stdin is untrusted: the id must
//	match sessionHookIDPattern and the payload's hook_event_name must equal
//	the argument before anything is sent. The harness value, PID and
//	timestamp are this command's own (the harness's JSON carries none of
//	them as the daemon needs them); the account is left empty.
//
// SPORT: cmd/cascade/fleet-sessions-hook (ADD) — P1-CORE-14.
package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	// sessionHookMaxStdin is the stdin read cap, 1 MiB. A payload larger
	// than this is refused whole, never truncated and parsed.
	sessionHookMaxStdin = 1 << 20
	// sessionHookDeadline bounds the one daemon round trip.
	sessionHookDeadline = time.Second
	// sessionHookHarness is the harness name the daemon records for these
	// sessions.
	sessionHookHarness = "claude-code"
	// sessionHookNotDelivered prefixes the one stderr line a failed post
	// prints; the error's kind follows it.
	sessionHookNotDelivered = "cascade: fleet session hook not delivered: "
)

// sessionHookIDPattern is the only shape of session id this command will
// forward: the harness's own ids (UUIDs) and ids built from letters,
// digits, underscore and hyphen, 1 to 128 bytes.
var sessionHookIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// sessionHookEnv carries the two process facts the command reads, injected
// so a test never depends on the real parent pid or wall clock.
type sessionHookEnv struct {
	Clock   runtime.Clock
	Getppid func() int
}

// productionSessionHookEnv reads the real parent pid and wall clock.
func productionSessionHookEnv() sessionHookEnv {
	return sessionHookEnv{Clock: runtime.NewSystemClock(), Getppid: os.Getppid}
}

// sessionHookInput is the slice of the harness's hook JSON this command
// decodes. Unknown fields are ignored on purpose: the harness adds fields
// between releases, and refusing a newer client would stop registering its
// sessions at exactly the moment someone upgrades.
type sessionHookInput struct {
	SessionID     string `json:"session_id"`
	HookEventName string `json:"hook_event_name"`
}

// newSessionsHookEventCmd builds the hidden `hook-event` command.
func newSessionsHookEventCmd(deps fleetSessionsDeps, env sessionHookEnv) *cobra.Command {
	return &cobra.Command{
		Use:    "hook-event <Event>",
		Short:  "Forward one harness hook event to the daemon (installed by the sessions hook pack)",
		Hidden: true,
		// Arbitrary arguments: an unknown event is "send nothing, exit 0",
		// not a usage error. Flag parsing is off for the same reason: a hook
		// must never exit 2 or print help, so `--bogus` and `--help` reach
		// sessionHookEventArg as ordinary (refused) arguments.
		DisableFlagParsing: true,
		Args:               cobra.ArbitraryArgs,
		PersistentPreRunE:  func(*cobra.Command, []string) error { return nil },
		SilenceUsage:       true,
		SilenceErrors:      true,
		RunE: func(cmd *cobra.Command, args []string) error {
			runSessionsHookEvent(cmd, deps, env, args)
			return nil
		},
	}
}

// runSessionsHookEvent is the whole command. It has no return value: no
// outcome of it is allowed to change the exit status.
func runSessionsHookEvent(cmd *cobra.Command, deps fleetSessionsDeps, env sessionHookEnv, args []string) {
	event, ok := sessionHookEventArg(args)
	if !ok {
		return
	}
	in, ok := readSessionHookInput(cmd.InOrStdin())
	if !ok || in.HookEventName != string(event) || !sessionHookIDPattern.MatchString(in.SessionID) {
		return
	}
	payload := hookpacks.HookPayload{
		Harness:     sessionHookHarness,
		EventType:   event,
		SessionID:   in.SessionID,
		PID:         env.Getppid(),
		Account:     "",
		TimestampMs: env.Clock.Now().UnixMilli(),
	}
	if err := postSessionHookEvent(cmd.Context(), deps, payload); err != nil {
		reportSessionHookFailure(cmd, err)
	}
}

// sessionHookEventArg returns the event argument when there is exactly one
// and it is one of the five events the sessions pack installs.
func sessionHookEventArg(args []string) (hookpacks.HookEventType, bool) {
	if len(args) != 1 {
		return "", false
	}
	for _, evt := range hookpacks.SessionsHookEvents() {
		if string(evt) == args[0] {
			return evt, true
		}
	}
	return "", false
}

// readSessionHookInput reads at most sessionHookMaxStdin bytes of stdin and
// decodes the fields this command uses. A payload over the cap, an empty
// one, or one that is not a JSON object yields false.
func readSessionHookInput(stdin io.Reader) (sessionHookInput, bool) {
	// Read one byte past the cap so "exactly at the cap" and "over it" are
	// distinguishable without buffering an unbounded stream.
	raw, err := io.ReadAll(io.LimitReader(stdin, sessionHookMaxStdin+1))
	if err != nil || len(raw) == 0 || len(raw) > sessionHookMaxStdin {
		return sessionHookInput{}, false
	}
	var in sessionHookInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return sessionHookInput{}, false
	}
	return in, true
}

// postSessionHookEvent sends payload through the client SDK with the
// one-second deadline the sessions pack's contract fixes.
func postSessionHookEvent(ctx context.Context, deps fleetSessionsDeps, payload hookpacks.HookPayload) error {
	settings, err := daemon.ResolveSettings(nil, deps.Paths)
	if err != nil {
		return err
	}
	c := client.New(settings.SocketPath, client.DialFunc(deps.DialContext), sessionHookDeadline)
	return c.Do(ctx, hookpacks.MethodHookEvent, payload, nil)
}

// reportSessionHookFailure writes the one stderr line a failed post gets.
// A write error is swallowed: there is nowhere left to report it.
func reportSessionHookFailure(cmd *cobra.Command, err error) {
	kind, ok := cascade.KindOf(err)
	name := "internal"
	if ok {
		name = kind.String()
	}
	_, _ = cmd.ErrOrStderr().Write([]byte(sessionHookNotDelivered + name + "\n"))
}
