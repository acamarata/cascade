// Purpose (this file): the generic non-Go-stack Affected path --
// operator-configured [ci].affected_cmd, run through the platform shell
// (an operator command STRING, the same trust model as [ci.local]'s own
// lint/test/build steps). runner_exec.go's ShellExecutor is deliberately
// NOT reused here: ExecRequest has no stdin field, and this ticket's
// files_scope does not name runner_exec.go/runner_types.go for a change
// that would widen it just to add one. This file's own minimal shell
// invocation mirrors ShellExecutor's shellFor/argv shape instead.
//
// Inputs: worktreeRoot, the configured command string, and the
// changed-path list, written to the command's stdin one path per line.
// Outputs: []Target, one per non-blank stdout line, or ErrAffectedCmdFailed
// for a non-zero exit (or a command that could not even start).
// Constraints: a failed affected_cmd is always a typed error, never a
// panic; the command runs with worktreeRoot as its working directory and
// a fixed timeout bounds it.
// SPORT: internal.ci.ErrAffectedCmdFailed/ADDED (P1-E32-W6-S65-T1).

package ci

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// affectedCmdTimeout bounds one affected_cmd invocation -- generous for
// a script that walks a dependency graph, short enough that a hung
// command cannot stall target selection indefinitely. [ci].affected_cmd
// carries no separate timeout key (full_desc names only the command
// itself), so this is a fixed constant, not a config-derived value.
const affectedCmdTimeout = 60 * time.Second

// ErrAffectedCmdFailed reports a configured [ci].affected_cmd that
// exited non-zero (or could not be started at all). Plain errors.New
// sentinel: see affected.go's Constraints note on why a plain sentinel,
// not a bare *cascade.Error, is what makes errors.Is(err,
// ErrAffectedCmdFailed) mean THIS failure specifically.
var ErrAffectedCmdFailed = errors.New("ci: affected_cmd exited non-zero")

// affectedCmdTargets runs command through the platform shell inside
// worktreeRoot, writing changed to its stdin (one path per line, per
// full_desc's stdin protocol), and parses its stdout as one target name
// per non-blank line.
func affectedCmdTargets(ctx context.Context, worktreeRoot, command string, changed []string) ([]Target, error) {
	runCtx, cancel := context.WithTimeout(ctx, affectedCmdTimeout)
	defer cancel()

	bin, flag := shellForAffectedCmd(goruntime.GOOS)
	cmd := exec.CommandContext(runCtx, bin, flag, command)
	cmd.Dir = worktreeRoot
	if bin == "cmd" {
		// Go's default windows argv escaping mangles an embedded quote
		// (e.g. a sed expression) before cmd.exe ever sees it --
		// shellcmdline_windows.go's setShellCmdLine replaces the command
		// line with the literal text instead.
		setShellCmdLine(cmd, command)
	}
	cmd.Stdin = strings.NewReader(strings.Join(changed, "\n") + "\n")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, errors.Join(ErrAffectedCmdFailed, err),
			"ci: affected_cmd %q in %q: %s", command, worktreeRoot, strings.TrimSpace(stderr.String()))
	}
	lines := splitNonEmptyLines(stdout.String())
	targets := make([]Target, len(lines))
	for i, l := range lines {
		targets[i] = Target(l)
	}
	return targets, nil
}

// shellForAffectedCmd mirrors runner_exec.go's shellFor: "sh -c" on
// darwin/linux, "cmd /C" on windows.
func shellForAffectedCmd(goos string) (bin, flag string) {
	if goos == "windows" {
		return "cmd", "/C"
	}
	return "sh", "-c"
}
