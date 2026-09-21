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
// own process group and the GROUP is signalled (runner_exec_unix.go), and
// cmd.WaitDelay bounds how long Wait will wait for the pipes after the
// process is gone.
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
	// GroupKillErr is non-nil when the deadline fired but this platform
	// could not reap the step's whole process tree -- see
	// runner_exec_windows.go. It is reported rather than hidden: the step
	// is still a failure either way, but an operator deserves to know a
	// grandchild may have outlived it.
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

// runViaShell is Run's testable core: bin/flag are parameters (not always
// goruntime.GOOS-derived) so runner_exec_test.go can prove the StartErr
// path -- a shell binary genuinely absent from PATH -- deterministically,
// without needing to sabotage the real "sh"/"cmd" this process has.
func runViaShell(ctx context.Context, bin, flag string, req ExecRequest) ExecResult {
	runCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, bin, flag, req.Command)
	cmd.Dir = req.WorkDir
	cmd.Env = stepEnv(req.Env)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// The group kill replaces CommandContext's default Cancel (which sends
	// Kill to the shell alone), and WaitDelay stops a surviving pipe holder
	// from outlasting the deadline. Both are set before Start so the
	// deadline cannot fire against a half-configured command.
	var killErr error
	setProcessGroup(cmd)
	cmd.Cancel = func() error {
		killErr = killProcessGroup(cmd)
		return killErr
	}
	cmd.WaitDelay = pipeDrainGrace

	err := cmd.Run()
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
	res.StartErr = cascade.Wrapf(cascade.KindUnavailable, err, "ci: starting command %q", req.Command)
	res.ExitCode = -1
	return res
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
