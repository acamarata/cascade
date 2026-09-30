// Purpose: sub-process invocation for the local CI runner: one shell
// command, an explicit working directory, an explicitly BUILT environment
// (runner_env.go's allowlist, never os.Environ() wholesale), and a
// per-step timeout that kills the whole process TREE rather than only the
// shell -- runner.go's engine depends on the Executor interface only,
// never exec.Command directly, so runner_test.go can prove the engine's
// sequencing/halt/journal logic with a fake that starts no real process
// (R-14.246-adjacent: a fake stands in for the EXTERNAL process only,
// never for the code under test -- ShellExecutor itself, the thing that
// actually forks, is exercised for real by this file's own _test.go).
//
// WHY THE TIMEOUT KILLS A GROUP, NOT A PROCESS. A step is a shell command,
// and shell commands background things: `make test &`, a dev server, a
// test harness that forks workers. exec.CommandContext's own cancellation
// signals the shell it started and nothing else, so on a deadline the
// shell dies and its children keep running -- and because cmd.Wait also
// waits for the output pipes to close, a surviving grandchild holding
// those pipes keeps Run blocked past the timeout it was supposed to
// enforce. Two mechanisms fix that together: the child is started in its
// own process group and the GROUP is signalled (runner_exec_unix.go; on
// windows the step runs in a Job Object that is terminated and waited for,
// runner_exec_windows.go), and cmd.WaitDelay bounds how long Wait will
// wait for the pipes after the process is gone.
//
// Inputs: an ExecRequest (working directory, command string interpreted by
// the platform shell, environment, timeout).
// Outputs: an ExecResult: exit code, captured stdout/stderr, a timeout
// flag, a StartErr for a command that could not be launched, and a
// GroupKillErr naming any platform that could not reap the tree.
// Constraints: cwd and env are ALWAYS set explicitly. No network egress:
// this file only forks a local sub-process (06 §5.15 L1).
// SPORT: internal.ci.Executor/ADDED, internal.ci.ShellExecutor/ADDED
//
//	(P1-E25-W5-S51-T5).

package ci

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	goruntime "runtime"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// pipeDrainGrace bounds how long cmd.Wait keeps waiting for the step's
// output pipes once the process itself has been killed. Without it, a
// grandchild that inherited stdout keeps Wait blocked indefinitely and the
// per-step timeout stops being a bound at all. It is deliberately short:
// by the time it applies, the tree has already been signalled.
const pipeDrainGrace = 500 * time.Millisecond

// gitRemoteTimeout bounds the one non-step command this package runs: the
// `git remote get-url origin` lookup that names the repository for the
// never-pay policy. A remote lookup is a local config read; a second is
// already generous.
const gitRemoteTimeout = 2 * time.Second

// ExecRequest is one command invocation's inputs.
type ExecRequest struct {
	WorkDir string
	Command string
	// Env is the complete environment the command runs with, already
	// filtered by AllowedEnv. A nil Env means "build one from this
	// process's own environment through the same allowlist" -- never "pass
	// everything through".
	Env     []string
	Timeout time.Duration
}

// Executor runs one shell command and reports its outcome. runner.go's
// engine holds an Executor, never a concrete exec.Cmd.
type Executor interface {
	Run(ctx context.Context, req ExecRequest) ExecResult
}

// ExecResult is one command invocation's outcome.
type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	// TimedOut is true when the command exceeded the request's timeout and
	// its process group was signalled.
	TimedOut bool
	// StartErr is non-nil only when the command could not be started at
	// all (e.g. the shell or the named program is absent from PATH).
	StartErr error
	// GroupKillErr is non-nil when the deadline fired but the step's whole
	// process tree could not be reaped (a unix group signal failed, or a
	// windows job still had members after its reap rounds). It is
	// reported rather than hidden: the step is still a failure either way,
	// but an operator deserves to know a grandchild may have outlived it.
	GroupKillErr error
}

// ShellExecutor is the real, production Executor: it forks the platform's
// shell (sh -c on darwin/linux, cmd /C on windows) in its own process
// group, with an explicit Dir and an explicitly built Env, bounded by a
// deadline that signals the group.
type ShellExecutor struct{}

// shellFor returns the platform shell binary and its "run this string"
// flag.
func shellFor(goos string) (bin, flag string) {
	if goos == "windows" {
		return "cmd", "/C"
	}
	return "sh", "-c"
}

// Run implements Executor.
func (ShellExecutor) Run(ctx context.Context, req ExecRequest) ExecResult {
	bin, flag := shellFor(goruntime.GOOS)
	return runViaShell(ctx, bin, flag, req)
}

// attachStep attaches a started step to its process tree. It is a
// variable only so a test can inject an attach failure; production
// code never reassigns it.
var attachStep = func(t processTree, cmd *exec.Cmd) error { return t.attach(cmd) }

// runViaShell is Run's testable core: bin/flag are parameters (not always
// goruntime.GOOS-derived) so runner_exec_test.go can prove the StartErr
// path -- a shell binary genuinely absent from PATH -- deterministically,
// without needing to sabotage the real "sh"/"cmd" this process has.
//
// The step's process tree is created before Start and released on return
// (on windows a Job Object whose close kills any survivor); the started
// child is attached to it before it runs. An attach failure has already
// killed the child, so it is reported as a StartErr once Wait reaps it.
func runViaShell(ctx context.Context, bin, flag string, req ExecRequest) ExecResult {
	runCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := shellCommand(runCtx, bin, flag, req, &stdout, &stderr)

	tree, err := newProcessTree()
	if err != nil {
		return startFailure(err, req.Command, "", "")
	}
	defer tree.close()

	// The tree kill replaces CommandContext's default Cancel (which sends
	// Kill to the shell alone), and WaitDelay stops a surviving pipe holder
	// from outlasting the deadline. Both are set before Start so the
	// deadline cannot fire against a half-configured command. os/exec's
	// Wait does not return before Cancel has returned, so a timed-out Run
	// returns only after the whole tree is gone.
	var killErr error
	setProcessGroup(cmd)
	cmd.Cancel = func() error {
		killErr = tree.kill(cmd)
		return killErr
	}
	cmd.WaitDelay = pipeDrainGrace

	if err := cmd.Start(); err != nil {
		return mapResult(runCtx, err, killErr, req.Command, &stdout, &stderr)
	}
	if err := attachStep(tree, cmd); err != nil {
		_ = cmd.Wait()
		return startFailure(err, req.Command, stdout.String(), stderr.String())
	}
	err = cmd.Wait()
	return mapResult(runCtx, err, killErr, req.Command, &stdout, &stderr)
}

// shellCommand builds the step's command: explicit Dir, explicitly built
// Env, and captured output.
func shellCommand(ctx context.Context, bin, flag string, req ExecRequest, stdout, stderr *bytes.Buffer) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, flag, req.Command)
	cmd.Dir = req.WorkDir
	cmd.Env = stepEnv(req.Env)
	if bin == "cmd" {
		// Go's default windows argv escaping mangles an embedded quote in
		// req.Command before cmd.exe ever sees it -- see
		// shellcmdline_windows.go's setShellCmdLine doc comment.
		setShellCmdLine(cmd, req.Command)
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd
}

// mapResult turns Start/Wait's outcome into an ExecResult: a deadline is
// TimedOut (with the tree kill's error), an exit error is its exit code,
// and anything else is a StartErr.
func mapResult(runCtx context.Context, err, killErr error, command string, stdout, stderr *bytes.Buffer) ExecResult {
	res := ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		res.TimedOut = true
		res.ExitCode = -1
		res.GroupKillErr = killErr
		return res
	}
	if err == nil {
		return res
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res
	}
	return startFailure(err, command, res.Stdout, res.Stderr)
}

// startFailure is the ExecResult of a step that never ran confined.
func startFailure(err error, command, stdout, stderr string) ExecResult {
	return ExecResult{
		Stdout:   stdout,
		Stderr:   stderr,
		ExitCode: -1,
		StartErr: cascade.Wrapf(cascade.KindUnavailable, err, "ci: starting command %q", command),
	}
}

// stepEnv returns the environment a step runs with. A caller-supplied Env
// is used verbatim (it already went through AllowedEnv); a nil Env is
// filled by applying the SAME allowlist to this process's own environment,
// so the "default" is still a filtered set and there is no path through
// this file that hands a step os.Environ() unfiltered.
func stepEnv(env []string) []string {
	if env != nil {
		return env
	}
	return AllowedEnv(os.Environ(), nil)
}
