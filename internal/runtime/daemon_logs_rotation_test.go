package runtime

// Purpose: the rotation-gap case for daemon_logs.go's follow mode, split
//   from daemon_logs_test.go under Art.10.3's 300-line cap.
// Constraints: deterministic -- the absence window is held open explicitly
//   rather than left to scheduling, so this test reports the code and not
//   the machine it ran on.
// SPORT: internal/runtime daemon-logs-rotation-gap/ADDED.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDaemonLogsHandler_FollowSurvivesTheRotationGap pins missingGraceTicks.
//
// A rotation is a rename FOLLOWED BY a create: two syscalls with a real
// window between them in which the path does not exist. The sibling test
// above closes that window as fast as the test goroutine can, so whether a
// poll lands inside it is a matter of scheduling luck -- it passed twenty
// consecutive local -race runs and failed on CI's loaded race lane, which
// is the worst kind of test, one that reports the machine rather than the
// code.
//
// This test makes the window WIDE and deterministic: the file stays absent
// for several poll intervals before the replacement appears. The follower
// must still report a ROTATION, because that is what happened. Drop
// missingGraceTicks to 0 and this fails with the deletion diagnostic.
func TestDaemonLogsHandler_FollowSurvivesTheRotationGap(t *testing.T) {
	const poll = 5 * time.Millisecond
	path := filepath.Join(t.TempDir(), "cascade.log")
	if err := writeFile(t, path, "before-rotation\n"); err != nil {
		t.Fatalf("seed log file: %v", err)
	}

	out := &syncBuffer{}
	diag := &syncBuffer{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- DaemonLogsHandler(ctx, DaemonLogsOptions{
			Path: path, Follow: true, Out: out, Diag: diag, PollInterval: poll,
		})
	}()

	if !waitForContains(t, out, "before-rotation", 2*time.Second) {
		t.Fatalf("initial content not delivered: %q", out.String())
	}

	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatalf("simulate rotation rename: %v", err)
	}
	// The gap a real rotation leaves, held open deliberately: two polls'
	// worth, comfortably inside the grace and far outside the sibling
	// test's incidental one.
	time.Sleep(2 * poll)
	if err := writeFile(t, path, "after-rotation\n"); err != nil {
		t.Fatalf("simulate post-rotation file: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("DaemonLogsHandler across the rotation gap: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("DaemonLogsHandler never exited after the rotation")
	}
	if got := diag.String(); !strings.Contains(got, "rotated") {
		t.Errorf("Diag = %q, want the ROTATION diagnostic; a transient absence during rotation must not be reported as a deletion", got)
	}
	if got := diag.String(); strings.Contains(got, "disappeared") {
		t.Errorf("Diag = %q, reported a rotation as a deleted file", got)
	}
}
