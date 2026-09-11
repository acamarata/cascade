// Purpose: the kill -9 fan-out integration test and the upgrade-in-place
//   test — split out of submit_test.go under R-14.117's authorized-split
//   allowance (Art.10.3's 300-line-per-file cap).
// Constraints: Art.7.1; TestResumeKillHelperProcess is re-executed as a
//   SEPARATE OS PROCESS (providers/sqlite/lock_crossprocess_test.go's own
//   established pattern) — never called directly by `go test` itself.
// SPORT: internal.fleet.resume.ResumeManager/ADDED (tests) (P1-E13-W3-S27-T2).

package resume

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
)

// crossProcessHelperEnv signals TestResumeKillHelperProcess to act as the
// killed writer rather than a no-op — the same pattern
// providers/sqlite/lock_crossprocess_test.go established.
const crossProcessHelperEnv = "CASCADE_RESUME_KILLTEST_DB_PATH"

// TestResumeKillHelperProcess is re-executed as a separate OS process by
// TestResumeKill9FanOut. Invoked as an ordinary `go test` run (the env var
// unset) it is a no-op.
func TestResumeKillHelperProcess(_ *testing.T) {
	path := os.Getenv(crossProcessHelperEnv)
	if path == "" {
		return
	}
	ctx := context.Background()
	driver, err := sqlite.Open(ctx, path)
	if err != nil {
		_, _ = os.Stdout.WriteString("OPEN_FAILED\n")
		return
	}
	store := journal.New(driver, testkit.NewFrozenClock(testInstant), journal.DefaultNamespace)
	req, _ := json.Marshal(provider.ModelRequest{TaskID: "killed-task", Inputs: []provider.ChatMessage{{Role: "user", Content: "hi"}}})
	cursorPayload, _ := json.Marshal(resumeCursorPayload{T: "cursor", TaskID: "killed-task", Legs: 2, Request: req})
	if _, err := store.Append(ctx, "killed-task", journal.KindResumeCursor, "cursor-op", cursorPayload); err != nil {
		_, _ = os.Stdout.WriteString("SEED_FAILED\n")
		return
	}
	// Leg 0 finishes; leg 1's fanout_leg_started lands but its
	// fanout_leg_done never will — this process is about to be SIGKILLed
	// mid-write, before it can append that entry or run any deferred
	// cleanup (driver.Close, in particular, never runs).
	done0, _ := json.Marshal(legPayload{LegIndex: 0, JobID: "job-0", Attempt: 1})
	if _, err := store.Append(ctx, "killed-task", journal.KindFanOutLegDone, "leg-done-0", done0); err != nil {
		_, _ = os.Stdout.WriteString("SEED_FAILED\n")
		return
	}
	started1, _ := json.Marshal(legPayload{LegIndex: 1, Attempt: 1})
	if _, err := store.Append(ctx, "killed-task", journal.KindFanOutLegStarted, "leg-started-1", started1); err != nil {
		_, _ = os.Stdout.WriteString("SEED_FAILED\n")
		return
	}
	_, _ = os.Stdout.WriteString("READY\n")
	_, _ = bufio.NewReader(os.Stdin).ReadByte() // block until SIGKILLed; never reached
}

// TestResumeKill9FanOut is the ticket-mandated kill -9 integration test.
// DISCLOSURE: the daemon process itself is not launched (that is
// cmd/cascade/daemon_unix.go's full composition); what is genuinely real
// here is the SIGKILL — a separate OS process, holding providers/sqlite's
// real exclusive lock and having written real journal entries through the
// real journal package, is killed with (*os.Process).Kill() (SIGKILL on
// darwin/linux) before it can append the surviving leg's completion or
// run any cleanup. The resumer then opens the SAME on-disk file. See
// testdata/README.md.
func TestResumeKill9FanOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.db")
	spawnAndKillHelper(t, path)

	// The resumer opens the SAME file the killed process held its
	// exclusive lock on. A stale lock from a SIGKILLed holder must not
	// deadlock this — the OS releases an flock/LockFileEx lock when its
	// holder's process dies, whatever it was doing; if that were false
	// this Open would hang or be refused, and the test's own timeout
	// (go test's default) would catch it.
	driver, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open after killed holder: %v (stale lock deadlocked the resumer)", err)
	}
	defer func() { _ = driver.Close() }()
	store := journal.New(driver, testkit.NewFrozenClock(testInstant), journal.DefaultNamespace)

	var calls []fakeFanOutCall
	mgr, err := New(store, journalingFanOut(&calls), nil, nil, nil, nil, "darwin")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := mgr.Run(context.Background())
	if err != nil {
		t.Fatalf("Run after kill -9: %v", err)
	}
	assertKilledTaskResumedOnce(t, report, calls)

	// Idempotent resume: running it again must not double-dispatch. The
	// cursor is now fully done as far as this test's fake exec reports
	// (both legs have a fanout_leg_done entry after the first Run), so a
	// second Run finds nothing left to resume for this task.
	report2, err := mgr.Run(context.Background())
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	for _, o := range report2.Outcomes {
		if o.EntityID == "killed-task" {
			t.Fatalf("second Run re-surfaced killed-task = %+v, want it absent (idempotent: fully done, no re-dispatch)", o)
		}
	}
}

// spawnAndKillHelper starts TestResumeKillHelperProcess against path,
// waits for its READY sentinel, and SIGKILLs (never asks it to exit
// cleanly) and reaps it — split out of TestResumeKill9FanOut to stay
// under the 50-line function cap (funlen).
func spawnAndKillHelper(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestResumeKillHelperProcess$")
	cmd.Env = append(os.Environ(), crossProcessHelperEnv+"="+path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	line, _ := bufio.NewReader(stdout).ReadString('\n')
	if line != "READY\n" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("helper process: want READY, got %q", line)
	}
	if err := cmd.Process.Kill(); err != nil { // SIGKILL, mid-write
		t.Fatalf("kill helper: %v", err)
	}
	_ = cmd.Wait() // reap
}

// journalingFanOut is a FanOutFunc test double that journals each leg it
// actually dispatches through the real appender argument, mirroring what
// conductor.FanOut's own dispatchLeg does in production (fanout.go) —
// needed so a SECOND Run can observe a leg's completion and correctly
// classify the task as fully done (idempotence). The bare fakeFanOut
// helper ignores the appender entirely, which would make a "nothing left
// to resume" assertion true for the wrong reason: not because resume is
// idempotent, but because the test double never recorded the effect.
func journalingFanOut(calls *[]fakeFanOutCall) FanOutFunc {
	return func(ctx context.Context, req provider.ModelRequest, n int, completed map[int]conductor.JobID, _ conductor.WithPermitFn, appender conductor.JournalAppender) ([]provider.ModelResponse, error) {
		*calls = append(*calls, fakeFanOutCall{req: req, n: n, completed: completed})
		resp := make([]provider.ModelResponse, n)
		for i := 0; i < n; i++ {
			if jobID, ok := completed[i]; ok {
				resp[i] = provider.ModelResponse{JobID: jobID}
				continue
			}
			_ = appender.AppendLeg(ctx, "fanout_leg_started", req.TaskID, i, nil)
			jobID := "job-" + itoa(uint64(i))
			_ = appender.AppendLeg(ctx, "fanout_leg_done", req.TaskID, i, map[string]string{"job_id": jobID})
			resp[i] = provider.ModelResponse{JobID: conductor.JobID(jobID)}
		}
		return resp, nil
	}
}

// assertKilledTaskResumedOnce checks the post-kill Run's outcome for the
// killed-task cursor: resumed, exactly one skipped-and-one-dispatched
// fanOut call.
func assertKilledTaskResumedOnce(t *testing.T, report Report, calls []fakeFanOutCall) {
	t.Helper()
	if len(report.Outcomes) != 1 {
		t.Fatalf("Outcomes = %+v, want exactly 1 (the killed-task cursor)", report.Outcomes)
	}
	got := report.Outcomes[0]
	if got.Classification != ClassResumable {
		t.Fatalf("Outcome = %+v, want ClassResumable (zero silent drops)", got)
	}
	if got.LegsDispatched != 1 {
		t.Fatalf("LegsDispatched = %d, want 1 (leg 0 already done and skipped, only leg 1 re-dispatched)", got.LegsDispatched)
	}
	if len(calls) != 1 || len(calls[0].completed) != 1 {
		t.Fatalf("fanOut calls = %+v, want 1 call with leg 0 in the completed map", calls)
	}
}

// TestResumeUpgradeInPlace exercises the §D-2 leg: the SAME store/Manager
// resuming after an ordinary (non-killed) restart, standing in for
// D/S-07.T5's drain+exec-relaunch — see testdata/README.md for the
// disclosed scope limit (no real daemon process is driven end to end
// here).
func TestResumeUpgradeInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.db")
	ctx := context.Background()

	driver1, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("sqlite.Open (pre-upgrade): %v", err)
	}
	store1 := journal.New(driver1, testkit.NewFrozenClock(testInstant), journal.DefaultNamespace)
	seedFanOutCursor(t, store1, "t-upgrade", 2, 0)

	var calls []fakeFanOutCall
	mgr1, err := New(store1, fakeFanOut(&calls, []provider.ModelResponse{{JobID: "job-1"}}, nil), nil, nil, nil, nil, "darwin")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := mgr1.Run(ctx); err != nil {
		t.Fatalf("Run before restart: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("fanOut calls before restart = %d, want 1", len(calls))
	}

	// Drain: the pre-upgrade process closes its store cleanly (D/S-07.T5's
	// drain, unlike TestResumeKill9FanOut's SIGKILL) and the new-version
	// process opens the identical file fresh.
	if err := driver1.Close(); err != nil {
		t.Fatalf("close pre-upgrade driver: %v", err)
	}

	driver2, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("sqlite.Open (post-upgrade): %v", err)
	}
	defer func() { _ = driver2.Close() }()
	store2 := journal.New(driver2, testkit.NewFrozenClock(testInstant), journal.DefaultNamespace)

	var calls2 []fakeFanOutCall
	mgr2, err := New(store2, fakeFanOut(&calls2, []provider.ModelResponse{{JobID: "job-2"}}, nil), nil, nil, nil, nil, "darwin")
	if err != nil {
		t.Fatalf("New after restart: %v", err)
	}
	report, err := mgr2.Run(ctx)
	if err != nil {
		t.Fatalf("Run after restart: %v", err)
	}
	for _, o := range report.Outcomes {
		if o.EntityID == "t-upgrade" && o.Classification != ClassResumable {
			t.Fatalf("post-upgrade outcome = %+v, want ClassResumable (surviving cursor re-submitted)", o)
		}
	}
	if len(calls2) != 1 {
		t.Fatalf("fanOut calls after restart = %d, want 1 (surviving cursor re-submitted)", len(calls2))
	}
}
