package ci

// Purpose: proves the root-cause fix for
// TestAffectedTargets_AffectedCmdPresent's windows/amd64 CI failure --
// setShellCmdLine keeps an embedded double quote (the shape any sed/awk
// [ci].affected_cmd or step command takes) intact all the way to cmd.exe,
// instead of Go's default windows argv escaping turning it into a `\"`
// sequence cmd.exe cannot parse.
// SPORT: internal.ci.setShellCmdLine/TESTED.

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// TestSetShellCmdLine_QuotedArgumentReachesChildIntact runs on every
// platform (the windows/amd64 CI lane runs untagged tests): on windows it
// spawns the real cmd.exe and proves the exact string after "echo "
// survives -- a mis-escaped `\"quoted\"` would show up in the captured
// output instead of the clean `"quoted"` a correctly delivered command
// line produces. On every other platform setShellCmdLine is a documented
// no-op (shellcmdline_unix.go), so the same call is exercised there for
// that no-op behavior instead, since real cmd.exe does not exist to spawn.
func TestSetShellCmdLine_QuotedArgumentReachesChildIntact(t *testing.T) {
	const command = `echo "quoted"`

	if runtime.GOOS != "windows" {
		cmd := exec.Command("true")
		setShellCmdLine(cmd, command)
		if cmd.SysProcAttr != nil {
			t.Fatalf("setShellCmdLine mutated SysProcAttr on %s, want a no-op (see shellcmdline_unix.go)", runtime.GOOS)
		}
		return
	}

	cmd := exec.Command("cmd", "/C", command)
	setShellCmdLine(cmd, command)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("cmd.Output: %v", err)
	}
	if got, want := strings.TrimSpace(string(out)), `"quoted"`; got != want {
		t.Fatalf("cmd.exe echoed %q, want %q -- the command line reached cmd.exe mangled", got, want)
	}
}
