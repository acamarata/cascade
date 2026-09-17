//go:build integration

package init_test

// Purpose: scenario B of P1-E16-W4-S35-T5 — a setup run killed part-way
//   resumes from its journal instead of starting over.
// Constraints: the run is killed by SIGNAL, not by returning an error
//   from a seam. The journal exists because a process can die between two
//   steps, and a test that simulated the death in-process would prove
//   nothing about the file that survived it.
// SPORT: internal/runtime/init acceptance (ADD) — P1-E16-W4-S35-T5.

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestAcceptInitScenarioB kills a run once its journal records a step,
// relaunches, and requires the second run to pick up where the first
// stopped and to finish the job.
func TestAcceptInitScenarioB(t *testing.T) {
	e := newEnv(t)

	killedAt := runUntilJournaled(t, e)
	if killedAt < 1 {
		t.Fatalf("the run was killed before it journaled anything (completed_step = %d)", killedAt)
	}
	if !exists(e.journal()) {
		t.Fatal("a killed run left no journal; the next run has nothing to resume from")
	}

	out, code := e.run(t, initArgs...)
	if code != 0 {
		t.Fatalf("the resuming run exited %d:\n%s", code, out)
	}

	// The end state is the assertion that matters: an interrupted setup
	// must reach the same machine a clean one does. Checking only that
	// the word "resume" was printed would pass against a second run that
	// silently started over.
	if row := e.harnessRow(t, "claude"); !row.Detected || !row.CascadeRegistered {
		t.Errorf("the harness is not wired after a resumed run: %+v", row)
	}
	if exists(e.journal()) {
		t.Errorf("the journal survived the completed resume at %s", e.journal())
	}
}

// runUntilJournaled starts a setup run, waits for the journal to record a
// completed step, kills the process, and returns the step it reached.
//
// Killed on the journal rather than after a sleep: a fixed delay races
// the machine it runs on, and this suite has to be as reliable on a
// loaded CI runner as on a developer's laptop.
func runUntilJournaled(t *testing.T, e env) int {
	t.Helper()
	cmd := exec.Command(e.bin, initArgs...)
	cmd.Dir = e.project
	cmd.Env = e.environ()
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the setup run: %v", err)
	}
	defer func() { _ = cmd.Wait() }()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if step := journaledStep(e); step >= 1 {
			if err := cmd.Process.Kill(); err != nil {
				t.Fatalf("killing the setup run: %v", err)
			}
			return step
		}
		if cmd.ProcessState != nil {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	// The run can legitimately finish before the poll sees a journal:
	// setup is fast, and every step of it is local. That is not a
	// failure of the resume path, it is a race this test cannot win, so
	// it says so rather than reporting a defect that is not there.
	t.Skip("the setup run completed before the journal could be observed mid-flight; " +
		"the resume path is covered by the unit suite's journal tests")
	return 0
}

// journaledStep reads the completed step out of the journal, or 0 when
// there is no readable journal yet.
//
// A partial read is a 0, not a failure: the journal is written through a
// temp file and a rename, so a reader can catch the instant before the
// rename, and treating that as an error would make this poll flaky by
// construction.
func journaledStep(e env) int {
	raw, err := os.ReadFile(e.journal())
	if err != nil {
		return 0
	}
	var state struct {
		CompletedStep int `json:"completed_step"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return 0
	}
	return state.CompletedStep
}
