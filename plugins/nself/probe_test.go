// Purpose: exercise the REAL execRunner — the thing that actually forks —
//
//	against a portable shell rather than against `nself` itself
//	(LANE-RULES' no-real-CLI-in-tests rule covers this plugin's own
//	subprocess). The working-directory case is the hermetic half of the
//	review's third finding: `pwd` reports what cmd.Dir was set to, so
//	dropping cmd.Dir turns this test red on any machine.
//
// SPORT: plugins/nself detect (TEST) — P1-E25-W5-S52-T2.

package nself

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// absentLookPath is a hermetic exec.LookPath stand-in: it never consults
// this machine's PATH.
func absentLookPath(string) (string, error) { return "", exec.ErrNotFound }

func TestExecRunner_RunsInTheDirectoryItIsGiven(t *testing.T) {
	dir := t.TempDir()
	// The shell that prints its own cwd natively: sh's pwd on windows is an
	// MSYS path (/c/Users/...), which is not the directory the child got.
	bin, args := "sh", []string{"-c", "pwd"}
	if runtime.GOOS == "windows" {
		bin, args = "cmd", []string{"/c", "cd"}
	}
	out, err := execRunner{}.Run(context.Background(), dir, bin, args, 5*time.Second)
	if err != nil {
		t.Fatalf("execRunner.Run(pwd) err = %v, want nil (is /bin/sh on PATH?)", err)
	}
	got, err := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", out, err)
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", dir, err)
	}
	if got != want {
		t.Fatalf("the child ran in %q, want the directory it was given, %q", got, want)
	}
}

func TestExecRunner_BinaryAbsentIsTypedAndHermetic(t *testing.T) {
	r := execRunner{locator: execLocator{goos: "plan9", lookPath: absentLookPath}}
	_, err := r.Run(context.Background(), t.TempDir(), "nself", probeArgs, time.Second)
	var absent *binaryAbsentError
	if !errors.As(err, &absent) {
		t.Fatalf("Run with an absent binary err = %v, want *binaryAbsentError", err)
	}
	if absent.GOOS != "plan9" || absent.Binary != "nself" {
		t.Fatalf("binaryAbsentError = %+v, want the injected platform and binary named", absent)
	}
	if got := absent.Error(); !strings.Contains(got, "plan9") || !strings.Contains(got, "nself") {
		t.Fatalf("binaryAbsentError.Error() = %q, want it to name both the binary and the platform", got)
	}
}

func TestExecRunner_RanAndFailedCarriesTheExitCodeAndNoChildOutput(t *testing.T) {
	const secret = "postgres://u:hunter2@127.0.0.1:5432/db"
	_, err := execRunner{}.Run(context.Background(), t.TempDir(), "sh",
		[]string{"-c", "echo " + secret + " >&2; exit 3"}, 5*time.Second)
	var failed *probeFailedError
	if !errors.As(err, &failed) {
		t.Fatalf("Run(exit 3) err = %v, want *probeFailedError", err)
	}
	if failed.ExitCode != 3 {
		t.Fatalf("probeFailedError.ExitCode = %d, want 3", failed.ExitCode)
	}
	if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "postgres://") {
		t.Fatalf("probe error = %q, want NO byte of the child's own output", err.Error())
	}
}

func TestExecRunner_TimeoutIsTypedAndBounded(t *testing.T) {
	_, err := execRunner{}.Run(context.Background(), t.TempDir(), "sh",
		[]string{"-c", "sleep 30"}, 50*time.Millisecond)
	var timedOut *probeTimeoutError
	if !errors.As(err, &timedOut) {
		t.Fatalf("Run(sleep 30, 50ms) err = %v, want *probeTimeoutError", err)
	}
	if timedOut.Timeout != 50*time.Millisecond {
		t.Fatalf("probeTimeoutError.Timeout = %v, want the bound it blew", timedOut.Timeout)
	}
}

// TestExecRunner_TimeoutHoldsWithABackgroundedPipeHolder proves the bound
// survives the case internal/ci's runner_exec.go documents: the child
// backgrounds a sleeper that inherits the output pipe. The group kill and
// cmd.WaitDelay are what make this return; this test asserts the OUTCOME
// (Run returns, typed, long before the sleeper would exit) rather than
// claiming to isolate which of the two did it.
func TestExecRunner_TimeoutHoldsWithABackgroundedPipeHolder(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		_, err := execRunner{}.Run(context.Background(), t.TempDir(), "sh",
			[]string{"-c", "sleep 30 & sleep 30"}, 100*time.Millisecond)
		done <- err
	}()
	select {
	case err := <-done:
		var timedOut *probeTimeoutError
		if !errors.As(err, &timedOut) {
			t.Fatalf("Run err = %v, want *probeTimeoutError", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return within 20s of a 100ms bound: the grandchild outlived the probe")
	}
}

func TestRunnerEnv_IsAClosedAllowlistPlusTheAutomationMarker(t *testing.T) {
	env := runnerEnv([]string{
		"PATH=/usr/bin",
		"HOME=/Users/someone",
		"GITHUB_TOKEN=should-never-be-forwarded",
		"AWS_SECRET_ACCESS_KEY=also-not",
		"LANG=en_US.UTF-8",
	})
	joined := strings.Join(env, "\n")
	for _, want := range []string{"PATH=/usr/bin", "HOME=/Users/someone", "LANG=en_US.UTF-8", "CASCADE_NO_INPUT=1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("runnerEnv() = %v, want it to carry %q", env, want)
		}
	}
	for _, forbidden := range []string{"GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("runnerEnv() forwarded %q; the allowlist is closed", forbidden)
		}
	}
}

func TestClassifyRunErr_SuccessIsNil(t *testing.T) {
	if err := classifyRunErr(context.Background(), "nself", time.Second, nil); err != nil {
		t.Fatalf("classifyRunErr(nil) = %v, want nil", err)
	}
}

func TestClassifyRunErr_StartFailureIsWrappedNotMisclassified(t *testing.T) {
	err := classifyRunErr(context.Background(), "nself", time.Second, errors.New("fork/exec: bad"))
	var failed *probeFailedError
	var timedOut *probeTimeoutError
	if errors.As(err, &failed) || errors.As(err, &timedOut) {
		t.Fatalf("classifyRunErr(start failure) = %v, want neither exit nor timeout classification", err)
	}
	if err == nil {
		t.Fatal("classifyRunErr(start failure) = nil, want a typed wrap")
	}
}

func TestTypedProbeErrors_MessagesNameTheirPayload(t *testing.T) {
	cases := map[error][]string{
		&binaryAbsentError{Binary: "nself", GOOS: "linux"}:            {"nself", "linux"},
		&probeTimeoutError{Binary: "nself", Timeout: 2 * time.Second}: {"nself", "2s"},
		&probeFailedError{Binary: "nself", ExitCode: 7}:               {"nself", "7"},
	}
	for err, wants := range cases {
		got := err.Error()
		for _, want := range wants {
			if !strings.Contains(got, want) {
				t.Errorf("%T.Error() = %q, want it to name %q", err, got, want)
			}
		}
	}
}

// TestKillProcessGroup_NoProcessAndAlreadyGone covers the two outcomes that
// are not failures: nothing was started, and the group exited between the
// deadline and the signal (ESRCH). The remaining branch — a signal refused
// for another reason, EPERM — is NOT covered here on purpose: every way to
// provoke it from a test signals a process group this test does not own.
func TestKillProcessGroup_NoProcessAndAlreadyGone(t *testing.T) {
	if err := killProcessGroup(&exec.Cmd{}); err != nil {
		t.Errorf("killProcessGroup(not started) = %v, want nil", err)
	}
	// A process that has already been waited for: its pid is reaped, so the
	// group signal reports ESRCH, which is success.
	cmd := exec.Command("sh", "-c", "exit 0")
	setProcessGroup(cmd)
	if err := cmd.Run(); err != nil {
		t.Fatalf("seed process: %v", err)
	}
	if err := killProcessGroup(cmd); err != nil {
		t.Errorf("killProcessGroup(already exited) = %v, want nil (ESRCH is success)", err)
	}
}
