//go:build windows

// Purpose: the windows half of the fix for a configured [ci].affected_cmd
// or step command containing embedded double quotes (e.g. a sed
// expression) arriving at cmd.exe mangled.
//
// WHY: exec.Command(bin, flag, command) on windows builds the child's
// command line by escaping each element of Args per the
// CommandLineToArgvW convention (an embedded `"` becomes `\"`, the whole
// argument gets wrapped in quotes if it contains a space). cmd.exe does
// not parse its command line that way: "/C" takes everything after it as
// one literal script line, so the re-escaped text arrives with `\"`
// sequences cmd.exe treats as two literal characters rather than a
// preserved quote -- this is exactly the "unterminated address regex"
// failure TestAffectedTargets_AffectedCmdPresent hit on the windows/amd64
// CI lane. Setting SysProcAttr.CmdLine bypasses argv escaping entirely:
// per syscall/exec_windows.go's StartProcess, a non-empty CmdLine is
// passed to CreateProcess's lpCommandLine verbatim, in place of the
// escaped-and-joined Args; lpApplicationName still comes from cmd.Path
// (resolved by exec.Command's LookPath), so which binary runs is
// unaffected.
//
// Inputs: cmd (already built via exec.Command(bin, flag, command), so
// cmd.Path/argv0 is already resolved) and command, the literal text that
// must follow "cmd /C " unmodified.
// Outputs: none; mutates cmd.SysProcAttr in place, preserving any field a
// caller (e.g. setProcessGroup, runner_exec_windows.go) already set.
// Constraints: callers gate on bin == "cmd" before calling this -- it is
// meaningless (and unverifiable, since only windows carries a CmdLine
// field) for any other shell.
// SPORT: internal.ci.setShellCmdLine/ADDED.

package ci

import (
	"os/exec"
	"syscall"
)

// setShellCmdLine overrides the windows command line exec.Cmd would
// otherwise build from Args with a literal "cmd /C <command>" line, so
// cmd.exe receives the operator-configured command exactly as configured.
func setShellCmdLine(cmd *exec.Cmd, command string) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CmdLine = `cmd /C ` + command
}
