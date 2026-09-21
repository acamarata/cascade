// Purpose: ShellExecutor tests. These are the ONE place in this ticket
// that spawns a REAL sub-process (per this ticket's own SECURITY/QUALITY
// note: a fake stands in for the external process only in runner_test.go
// -- ShellExecutor itself, the thing that actually forks, must be proven
// against reality). Every real invocation here targets a portable
// builtin (sh -c "exit N") or a deliberately-absent binary name -- never
// a network call, matching this ticket's "no new outbound network class"
// scope.
// SPORT: internal.ci.ShellExecutor/TESTED (P1-E25-W5-S51-T5).
package ci

import (
	"context"
	"os"
	goruntime "runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// stepReq builds an ExecRequest for a real-subprocess test: a temp working
// directory and the allowlisted environment a production step gets.
func stepReq(t *testing.T, command string, timeout time.Duration) ExecRequest {
	t.Helper()
	return ExecRequest{
		WorkDir: t.TempDir(),
		Command: command,
		Env:     AllowedEnv(testEnviron(), nil),
		Timeout: timeout,
	}
}

func TestShellExecutor_Success(t *testing.T) {
	res := ShellExecutor{}.Run(context.Background(), stepReq(t, exitCommand(0), time.Second))
	if res.ExitCode != 0 || res.StartErr != nil || res.TimedOut {
		t.Fatalf("Run(exit 0) = %+v, want a clean zero exit", res)
	}
}

func TestShellExecutor_NonZeroExit(t *testing.T) {
	res := ShellExecutor{}.Run(context.Background(), stepReq(t, exitCommand(3), time.Second))
	if res.ExitCode != 3 || res.StartErr != nil || res.TimedOut {
		t.Fatalf("Run(exit 3) = %+v, want ExitCode 3", res)
	}
}

// TestShellExecutor_CommandNotFound proves the "actionable error,
// non-zero exit" acceptance criterion for a command absent from PATH:
// the platform shell itself starts fine (StartErr is nil -- "sh"/"cmd"
// are real, present binaries) and reports the standard shell
// command-not-found exit code as a non-zero ExitCode, which
// runner.go's Execute treats as a failed step exactly like any other
// non-zero exit.
func TestShellExecutor_CommandNotFound(t *testing.T) {
	res := ShellExecutor{}.Run(context.Background(), stepReq(t, "definitely-not-a-real-binary-xyz-12345", time.Second))
	if res.ExitCode == 0 {
		t.Fatalf("Run(missing binary) = %+v, want a non-zero exit code", res)
	}
	if res.TimedOut {
		t.Error("a missing binary must not report TimedOut")
	}
}

// TestShellExecutor_Timeout proves a command exceeding its timeout is
// killed and reported as TimedOut, never left to hang the caller.
func TestShellExecutor_Timeout(t *testing.T) {
	res := ShellExecutor{}.Run(context.Background(), stepReq(t, sleepCommand(5), 50*time.Millisecond))
	if !res.TimedOut {
		t.Fatalf("Run(sleep 5s, timeout 50ms) = %+v, want TimedOut", res)
	}
	if res.StartErr != nil {
		t.Errorf("a timeout must not also report a StartErr: %v", res.StartErr)
	}
}

// TestShellExecutor_EnvIsAllowlisted is the leak test: a GITHUB_TOKEN and a
// CASCADE_* variable set in THIS process must not appear in a step's
// environment. It reads the real step's own view (`env`), not a recorded
// call, so it proves what the sub-process actually received.
func TestShellExecutor_EnvIsAllowlisted(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp-should-never-reach-a-step")
	t.Setenv("CASCADE_HOME", "/should/never/reach/a/step")

	req := ExecRequest{
		WorkDir: t.TempDir(),
		Command: envDumpCommand(),
		Env:     AllowedEnv(os.Environ(), nil),
		Timeout: 5 * time.Second,
	}
	res := ShellExecutor{}.Run(context.Background(), req)
	if res.StartErr != nil || res.ExitCode != 0 {
		t.Fatalf("env dump = %+v, want a clean run", res)
	}
	// The step's own environment dump is NOT echoed into a failure message:
	// on a real regression it is full of the developer's (or the runner's)
	// credentials, which is exactly what must not end up in a CI log. The
	// assertions name the offending variable instead.
	for _, leaked := range []string{"GITHUB_TOKEN", "CASCADE_HOME", "ghp-should-never-reach-a-step"} {
		if strings.Contains(res.Stdout, leaked) {
			t.Errorf("a step's environment carried %q; the allowlist must build the environment, never inherit it", leaked)
		}
	}
	if !strings.Contains(res.Stdout, "CI=true") {
		t.Error("a step's environment did not carry CI=true")
	}
}

// TestRunViaShell_StartErr proves the StartErr path directly: a shell
// BINARY that does not exist at all (distinct from a command the real
// shell fails to find) is reported as a start error, never masked as a
// zero exit or a panic.
func TestRunViaShell_StartErr(t *testing.T) {
	res := runViaShell(context.Background(), "definitely-not-a-real-shell-xyz-12345", "-c",
		ExecRequest{WorkDir: t.TempDir(), Command: "true", Timeout: time.Second})
	if res.StartErr == nil {
		t.Fatal("expected a StartErr for a nonexistent shell binary")
	}
	if res.ExitCode == 0 {
		t.Error("a StartErr must carry a non-zero ExitCode")
	}
}

// TestStepEnv_NilRequestEnvIsStillFiltered proves the default path cannot
// become an os.Environ() pass-through: with no Env supplied, the step still
// gets the allowlisted set.
func TestStepEnv_NilRequestEnvIsStillFiltered(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp-should-never-reach-a-step")
	for _, kv := range stepEnv(nil) {
		if strings.HasPrefix(kv, "GITHUB_TOKEN=") {
			t.Fatal("stepEnv(nil) passed GITHUB_TOKEN through; the default must still be filtered")
		}
	}
}

func TestShellFor(t *testing.T) {
	if bin, flag := shellFor("windows"); bin != "cmd" || flag != "/C" {
		t.Errorf("shellFor(windows) = (%q, %q), want (cmd, /C)", bin, flag)
	}
	if bin, flag := shellFor("darwin"); bin != "sh" || flag != "-c" {
		t.Errorf("shellFor(darwin) = (%q, %q), want (sh, -c)", bin, flag)
	}
	if bin, flag := shellFor("linux"); bin != "sh" || flag != "-c" {
		t.Errorf("shellFor(linux) = (%q, %q), want (sh, -c)", bin, flag)
	}
}

// exitCommand, sleepCommand and envDumpCommand render a portable command
// string for the CURRENT test process's own GOOS. This suite runs on
// darwin/linux in CI; the windows branches are written out rather than
// assumed away, and where a builtin is genuinely spelled the same on both
// (`exit N`) there is deliberately no branch at all.
func exitCommand(code int) string {
	return "exit " + strconv.Itoa(code)
}

func sleepCommand(seconds int) string {
	if goruntime.GOOS == "windows" {
		return "timeout /T " + strconv.Itoa(seconds)
	}
	return "sleep " + strconv.Itoa(seconds)
}

func envDumpCommand() string {
	if goruntime.GOOS == "windows" {
		return "set"
	}
	return "env"
}

// testEnviron is a fixed, minimal ambient environment for the tests that
// only need a step to run at all: PATH so the shell can find a binary, and
// nothing else. Deliberately not os.Environ() -- a test that passed the
// real environment through could not prove the allowlist does anything.
func testEnviron() []string {
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
}
