// Purpose: StepResult.Passed/RunResult.Passed unit tests.
// SPORT: internal.ci.StepResult/TESTED, internal.ci.RunResult/TESTED
//
//	(P1-E25-W5-S51-T5).
package ci

import (
	"errors"
	"testing"
)

func TestStepResult_Passed(t *testing.T) {
	cases := []struct {
		name string
		sr   StepResult
		want bool
	}{
		{"zero exit", StepResult{ExitCode: 0}, true},
		{"non-zero exit", StepResult{ExitCode: 1}, false},
		{"timed out with zero exit", StepResult{ExitCode: 0, TimedOut: true}, false},
		{"start error with zero exit", StepResult{ExitCode: 0, StartErr: errors.New("boom")}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.sr.Passed(); got != c.want {
				t.Errorf("Passed() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestRunResult_Passed(t *testing.T) {
	if !(RunResult{}).Passed() {
		t.Error("a zero-value RunResult (no FailedStep) must report Passed()")
	}
	if (RunResult{FailedStep: StepTest}).Passed() {
		t.Error("a RunResult with FailedStep set must not report Passed()")
	}
}

// The run-id allocator and LocalRepoID moved to runner_ids.go when the
// clock-derived id was replaced by a store allocation; their tests live in
// runner_ids_test.go beside them.
