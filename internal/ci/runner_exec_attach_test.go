// Purpose: proves the attach-error branch of runViaShell on every OS. An
// injected attach failure must fail the step closed: StartErr wraps the
// injected error, the shell is dead and reaped before Run returns, and no
// exit code or timeout is reported as if the step had run.
// SPORT: internal.ci.runViaShell/TESTED.
package ci

import (
	"context"
	"errors"
	"os/exec"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// errAttachInjected is the failure the injected attachStep returns.
var errAttachInjected = errors.New("attach injected")

func TestRunViaShellAttachErrorFailsClosed(t *testing.T) {
	saved := attachStep
	t.Cleanup(func() { attachStep = saved })

	var started *exec.Cmd
	attachStep = func(_ processTree, cmd *exec.Cmd) error {
		started = cmd
		if err := cmd.Process.Kill(); err != nil {
			t.Errorf("killing the started shell: %v", err)
		}
		return errAttachInjected
	}

	bin, flag := shellFor(goruntime.GOOS)
	res := runViaShell(context.Background(), bin, flag, stepReq(t, sleepCommand(30), 10*time.Second))

	if !errors.Is(res.StartErr, errAttachInjected) {
		t.Fatalf("StartErr = %v, want it to wrap the injected error", res.StartErr)
	}
	var ce *cascade.Error
	if !errors.As(res.StartErr, &ce) || ce.Kind != cascade.KindUnavailable {
		t.Errorf("StartErr = %v, want a KindUnavailable cascade.Error", res.StartErr)
	}
	msg := res.StartErr.Error()
	if !strings.Contains(msg, "ci: starting command") || !strings.Contains(msg, errAttachInjected.Error()) {
		t.Errorf("StartErr message = %q, want the start prefix and the injected text", msg)
	}
	if res.TimedOut || res.ExitCode != -1 {
		t.Errorf("TimedOut=%v ExitCode=%d, want false and -1", res.TimedOut, res.ExitCode)
	}
	if started == nil || started.ProcessState == nil || started.ProcessState.Success() {
		t.Errorf("shell not reaped as failed before Run returned: %+v", started)
	}
}
