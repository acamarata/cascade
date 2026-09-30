//go:build windows

// Purpose: the windows process-TREE timeout proof. The step's shell starts
// a background ping and a foreground ping, both with the step's WorkDir as
// their current directory. A shell-only kill leaves the background ping
// holding the directory, so RemoveAll fails; the Job Object reap must
// leave nothing behind by the time Run returns.
// SPORT: internal.ci.processTree/TESTED.
package ci

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestShellExecutor_TimeoutReapsTreeWindows(t *testing.T) {
	req := ExecRequest{
		WorkDir: t.TempDir(),
		Command: "start /B ping -n 30 127.0.0.1 >NUL & ping -n 30 127.0.0.1 >NUL",
		Env:     AllowedEnv(testEnviron(), nil),
		Timeout: 2 * time.Second,
	}
	res := ShellExecutor{}.Run(context.Background(), req)
	if !res.TimedOut {
		t.Fatalf("Run = %+v, want TimedOut", res)
	}
	if res.GroupKillErr != nil {
		t.Fatalf("GroupKillErr = %v, want nil (the job reaped the tree)", res.GroupKillErr)
	}
	if err := os.RemoveAll(req.WorkDir); err != nil {
		t.Fatalf("RemoveAll(WorkDir) right after Run = %v, want nil: a step process still holds it", err)
	}
}
