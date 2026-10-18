// Purpose: registers the hidden `hook` root mount and builds
//
//	`cascade hook relay <event>`, the command the agent hook
//	pack installs for PreToolUse, PostToolUse and Stop. It reads the
//	harness's hook JSON on stdin and sends one authenticated frame to the
//	driver's per-spawn callback channel (agents.Relay).
//
// Inputs: the event name as the only argument; the hook JSON on stdin; the
//
//	spawn's project directory from CLAUDE_PROJECT_DIR in the process
//	environment (set by the harness, never taken from the payload or the
//	working directory) and the uid of the process.
//
// Outputs: nothing on stdout. Exit 0 when a frame was accepted, when
//
//	CLAUDE_PROJECT_DIR is unset or empty, or when there is no callback file
//	(a hook outside a spawn does nothing); non-zero, with one
//	error line, when the callback file or socket is refused or the driver
//	refuses the frame.
//
// Constraints: never exits 2 (the harness reads 2 from a PreToolUse hook as
//
//	"block this tool call"), so an input error is reported as a different
//	kind and a panic is recovered as KindInternal. The deadline is taken
//	from the injected Clock. Flag parsing is off so `--help` is an argument, not a usage exit.
//	The root's daemonless probe is skipped: a hook must not print or dial the
//	daemon. The token is read from the callback file only; this file never
//	sees it.
//
// SPORT: cmd/cascade root mounts (P1-AGT-14, contract:agent-hook-relay).
package main

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// hookRelayDeadline bounds one relay (lock wait, dial, ack); the pack sets a
// longer harness timeout.
const hookRelayDeadline = 4 * time.Second

// projectDirVar names the harness's project directory, the leased tree root
// where the driver writes the callback file.
const projectDirVar = "CLAUDE_PROJECT_DIR"

// hookRelayEnv carries the process facts, the clock the deadline is taken
// from and the relay itself, injected so a test never touches the real
// environment, the wall clock or a real driver.
type hookRelayEnv struct {
	Getenv  func(string) string
	Getuid  func() int
	Relay   func(ctx context.Context, projectDir, event string, stdin io.Reader, uid int) error
	Clock   runtime.Clock
	Timeout time.Duration
}

// productionHookRelayEnv wires the real process and agents.Relay.
func productionHookRelayEnv() hookRelayEnv {
	return hookRelayEnv{
		Getenv: os.Getenv, Getuid: os.Getuid, Relay: agents.Relay,
		Clock: runtime.NewSystemClock(), Timeout: hookRelayDeadline,
	}
}

// mountHookRelayCmd adds the hidden `hook` noun to root.
func mountHookRelayCmd(root *cobra.Command) {
	hook := &cobra.Command{Use: "hook", Short: "Harness hook helpers (installed by the agent hook pack)", Hidden: true}
	hook.AddCommand(newHookRelayCmd(productionHookRelayEnv()))
	root.AddCommand(hook)
}

// newHookRelayCmd builds `relay <event>`.
func newHookRelayCmd(env hookRelayEnv) *cobra.Command {
	return &cobra.Command{
		Use:                "relay <event>",
		Short:              "Relay one harness hook event to the driver's callback channel",
		Hidden:             true,
		DisableFlagParsing: true,
		Args:               cobra.ArbitraryArgs,
		PersistentPreRunE:  func(*cobra.Command, []string) error { return nil },
		SilenceUsage:       true,
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			defer recoverRelayPanic(&err)
			return notExitTwo(runHookRelay(cmd, env, args))
		},
	}
}

// recoverRelayPanic turns a panic into a KindInternal error (exit 1): an
// uncaught Go panic exits 2, which the harness reads from a PreToolUse hook
// as "block this tool call". The panic value is not echoed, so nothing the
// relay held can reach stderr.
func recoverRelayPanic(err *error) {
	if r := recover(); r != nil {
		*err = cascade.New(cascade.KindInternal, "hook relay: internal failure")
	}
}

// runHookRelay validates the argument and runs the relay under the deadline
// for the project directory the harness named; with none, the hook is
// outside a spawn and nothing is sent.
func runHookRelay(cmd *cobra.Command, env hookRelayEnv, args []string) error {
	if len(args) != 1 {
		return cascade.New(cascade.KindUnsupported, "hook relay: exactly one event name is required")
	}
	projectDir := env.Getenv(projectDirVar)
	if projectDir == "" {
		return nil
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithDeadline(ctx, env.Clock.Now().Add(env.Timeout))
	defer cancel()
	return env.Relay(ctx, projectDir, args[0], cmd.InOrStdin(), env.Getuid())
}

// notExitTwo re-labels an invalid-input error so its exit status is not 2.
func notExitTwo(err error) error {
	if err != nil && cascade.ExitCode(err) == cascade.ExitInvalidInput {
		return cascade.Wrap(cascade.KindUnsupported, err, "hook relay: input refused")
	}
	return err
}

var _ = registerRootMount(rootMount{Name: "hook", Order: 267, Mount: mountHookRelayCmd})
