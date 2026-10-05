// Purpose: the kill -9 fan-out integration test and the upgrade-in-place
//   test — split out of submit_test.go under R-14.117's authorized-split
//   allowance (Art.10.3's 300-line-per-file cap). Both now prove the
//   classify-only rule (EPIC Decision 12): a surviving fan-out cursor is
//   reported Resumable and nothing is appended or dispatched for it.
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
	cursorPayload, _ := encodeCursor(FanOutCursor{FanOutID: "killed-task", TaskID: "killed-task", Legs: 2})
	if _, err := store.Append(ctx, FanOutEntity("killed-task"), journal.KindResumeCursor, "cursor-op", cursorPayload); err != nil {
		_, _ = os.Stdout.WriteString("SEED_FAILED\n")
		return
	}
	// Leg 0 finishes; leg 1's fanout_leg_started lands but its
	// fanout_leg_done never will — this process is about to be SIGKILLed
	// mid-write, before it can append that entry or run any deferred
	// cleanup (driver.Close, in particular, never runs).
	done0, _ := json.Marshal(legPayload{LegIndex: 0, JobID: "job-0", Attempt: 1, Outcome: conductor.LegOutcomeOK})
	if _, err := store.Append(ctx, FanOutEntity("killed-task"), journal.KindFanOutLegDone, "leg-done-0", done0); err != nil {
		_, _ = os.Stdout.WriteString("SEED_FAILED\n")
		return
	}
	started1, _ := json.Marshal(legPayload{LegIndex: 1, Attempt: 1})
	if _, err := store.Append(ctx, FanOutEntity("killed-task"), journal.KindFanOutLegStarted, "leg-started-1", started1); err != nil {
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

	mgr, err := New(store, nil, nil, "darwin")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Resume classifies the surviving cursor and dispatches nothing, so a
	// second Run (a second restart with no re-attach) sees the identical
	// journal and the identical verdict: no double dispatch, no drop.
	for run := 1; run <= 2; run++ {
		report, err := mgr.Run(context.Background())
		if err != nil {
			t.Fatalf("Run %d after kill -9: %v", run, err)
		}
		assertClassifiedOnly(t, store, report, FanOutEntity("killed-task"), 3)
	}
}

// spawnAndKillHelper starts TestResumeKillHelperProcess against path,
// waits for its READY sentinel, and SIGKILLs (never asks it to exit
// cleanly) and reaps it — split out of TestResumeKill9FanOut to stay
// under the 50-line function cap (funlen).
//
// The helper's blocking read is wired to an os.Pipe whose write end this
// function holds open for the helper's whole lifetime (never written to,
// closed only by cmd.Wait's process cleanup). Leaving cmd.Stdin unset
// would connect the child to the null device instead, which reads as an
// IMMEDIATE EOF rather than blocking -- the child would then race its own
// clean exit against this function's Kill() call, invisible on POSIX
// (Kill on an already-exited-but-unreaped process is a silent no-op) but
// "TerminateProcess: Access is denied" on Windows. A held-open pipe
// guarantees the helper is still genuinely blocked in ReadByte when the
// SIGKILL lands, on every platform.
func spawnAndKillHelper(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestResumeKillHelperProcess$")
	cmd.Env = append(os.Environ(), crossProcessHelperEnv+"="+path)
	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	defer func() { _ = stdinWriter.Close() }()
	cmd.Stdin = stdinReader
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	_ = stdinReader.Close() // the child holds its own duplicated handle
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

// assertClassifiedOnly checks that report holds exactly one outcome, entity
// Resumable with no error, and that entity still replays want entries: no
// fence marker, no leg entry, nothing dispatched.
func assertClassifiedOnly(t *testing.T, store journal.Store, report Report, entity string, want int) {
	t.Helper()
	if len(report.Outcomes) != 1 {
		t.Fatalf("Outcomes = %+v, want exactly 1 (%s)", report.Outcomes, entity)
	}
	if got := report.Outcomes[0]; got.EntityID != entity || got.Classification != ClassResumable || got.Err != nil {
		t.Fatalf("Outcome = %+v, want %s ClassResumable with no error (zero silent drops)", got, entity)
	}
	if n := entryCount(t, store, entity); n != want {
		t.Fatalf("%s replays %d entries after Run, want %d (classify-only: nothing appended or dispatched)", entity, n, want)
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

	mgr1, err := New(store1, nil, nil, "darwin")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := mgr1.Run(ctx)
	if err != nil {
		t.Fatalf("Run before restart: %v", err)
	}
	assertClassifiedOnly(t, store1, report, FanOutEntity("t-upgrade"), 2)

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

	mgr2, err := New(store2, nil, nil, "darwin")
	if err != nil {
		t.Fatalf("New after restart: %v", err)
	}
	report, err = mgr2.Run(ctx)
	if err != nil {
		t.Fatalf("Run after restart: %v", err)
	}
	assertClassifiedOnly(t, store2, report, FanOutEntity("t-upgrade"), 2)
}
