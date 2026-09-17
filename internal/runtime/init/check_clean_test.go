package init

// Purpose (this file): the one thing `--check` exists to say — that there
//   is nothing left to do.
//
// WHY IT NEEDED ITS OWN FILE. Every existing --check test runs against a
//   VIRGIN home, where the plan is long and the exit code is 3 for half a
//   dozen real reasons. The case nobody covered is the converged one, and
//   that is the case the flag is for: an operator scripting on exit 3
//   reconverges forever if it can never be 0. It could not be, because
//   step 9 planned the first-run health check unconditionally — a check
//   that changes nothing, counted as a change (R-14.277, found by the W-4
//   hardening gate against the shipped artifact).
// SPORT: runtime/init --check clean verdict (ADD tests) — P1-E19-W4-S42-T7.

import (
	"context"
	"strings"
	"testing"
)

// TestCheckReportsNothingToDoOnAConvergedMachine is the assertion the
// defect failed: with every step's work already done or deliberately
// skipped, the plan is EMPTY.
func TestCheckReportsNothingToDoOnAConvergedMachine(t *testing.T) {
	// Nothing to wire, nothing to install: no harness detected, no
	// catalog entry, and the daemon skipped the way `--no-daemon` skips
	// it. What is left is the doctor step, which writes nothing.
	w, rec, out, _ := fixture(t, Options{Mode: ModeCheck, NoDaemon: true}, nil)
	rec.detected = nil
	rec.entries = nil

	report, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run(--check): %v", err)
	}
	if len(report.Diff) != 0 {
		t.Fatalf("--check plans %v on a machine with nothing outstanding; an operator scripting "+
			"on its exit code would reconverge forever", report.Diff)
	}
	// The doctor step still SAYS what it would do — the operator loses no
	// information, only the false claim that it is a pending change.
	if !strings.Contains(out.String(), "doctor") {
		t.Errorf("the doctor step vanished from the report entirely:\n%s", out)
	}
	if rec.doctorRan {
		t.Error("--check ran the health check")
	}
}

// TestCheckStillPlansRealWork is the other half, without which the test
// above would pass for a --check that planned nothing ever.
func TestCheckStillPlansRealWork(t *testing.T) {
	w, _, _, _ := fixture(t, Options{Mode: ModeCheck}, nil)
	report, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run(--check): %v", err)
	}
	if len(report.Diff) == 0 {
		t.Fatal("--check on a virgin home planned nothing")
	}
	joined := strings.Join(report.Diff, "\n")
	for _, want := range []string{"wire claude", "install the cascade daemon"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the plan omits %q:\n%s", want, joined)
		}
	}
	// And the health check is not among them: it is not a change.
	if strings.Contains(joined, "doctor") {
		t.Errorf("the plan counts the health check as a change:\n%s", joined)
	}
}
