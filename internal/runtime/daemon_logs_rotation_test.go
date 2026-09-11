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
	"sync"
	"testing"
	"time"
)

// rotationFakeTicker is a manually-driven Ticker, confined to this file
// per Art.1 (metrics_emitter_test.go's fakeTicker is confined to its own
// file the same way). Each Tick blocks until followLoop's select receives
// it, so a sequence of Tick calls deterministically orders "the follower
// processed exactly N polls" against file-system operations the test
// performs between them -- no wall-clock window to land inside or miss.
type rotationFakeTicker struct {
	c        chan struct{}
	stopOnce sync.Once
}

func newRotationFakeTicker() *rotationFakeTicker {
	return &rotationFakeTicker{c: make(chan struct{})}
}

func (f *rotationFakeTicker) C() <-chan struct{} { return f.c }

func (f *rotationFakeTicker) Stop() { f.stopOnce.Do(func() {}) }

// Tick fires one poll, blocking until followLoop's select consumes it or
// ctx ends first (so a test never hangs forever if followLoop already
// exited for an unrelated reason).
func (f *rotationFakeTicker) Tick(ctx context.Context) {
	select {
	case f.c <- struct{}{}:
	case <-ctx.Done():
	}
}

// TestDaemonLogsHandler_FollowSurvivesTheRotationGap pins missingGraceTicks.
//
// A rotation is a rename FOLLOWED BY a create: two syscalls with a real
// window between them in which the path does not exist. Driving that
// window with a real Sleep against a real PollInterval made whether a
// poll landed inside it a matter of scheduling luck -- it passed twenty
// consecutive local -race runs and failed on CI's loaded race lane, which
// is the worst kind of test, one that reports the machine rather than the
// code.
//
// This version drives the poll loop with an injected Ticker instead: it
// fires exactly missingGraceTicks polls while the path is absent (proving
// the FULL grace period survives, not just "some window wide enough on
// this machine"), only THEN creates the replacement file, and fires one
// more poll to have the follower observe it. The follower must still
// report a ROTATION, because that is what happened. Drop missingGraceTicks
// to 0 and this fails with the deletion diagnostic on the very first tick.
func TestDaemonLogsHandler_FollowSurvivesTheRotationGap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.log")
	if err := writeFile(t, path, "before-rotation\n"); err != nil {
		t.Fatalf("seed log file: %v", err)
	}

	out, diag, ctx, cancel, ticker, done := startRotationFollower(t, path)
	defer cancel()

	if !waitForContains(t, out, "before-rotation", 2*time.Second) {
		t.Fatalf("initial content not delivered: %q", out.String())
	}

	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatalf("simulate rotation rename: %v", err)
	}

	// Fire exactly wantGraceTicks polls while the path is absent -- the
	// intended production grace period, HARDCODED here rather than read
	// from missingGraceTicks itself. Reading the production constant
	// would make this loop shrink to zero iterations the moment someone
	// mutates missingGraceTicks to 0, which would let the very first
	// (only) tick land AFTER the replacement file already exists and
	// report "rotated" by accident -- passing for the wrong reason and
	// hiding exactly the regression this test exists to catch. Each of
	// these must land in the "missing, but still within grace" branch:
	// the follower must not exit here, or the final select below times
	// out.
	const wantGraceTicks = 3
	for i := 0; i < wantGraceTicks; i++ {
		ticker.Tick(ctx)
	}

	if err := writeFile(t, path, "after-rotation\n"); err != nil {
		t.Fatalf("simulate post-rotation file: %v", err)
	}
	// One more poll: the path now resolves again, to a DIFFERENT file
	// (per os.SameFile), which is the rotation diagnostic path.
	ticker.Tick(ctx)

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

// startRotationFollower starts DaemonLogsHandler in follow mode against
// path, paced by a manually driven ticker rather than wall-clock time.
//
// It exists only to keep the test that uses it under the 50-line function
// cap; every value it returns is used by that test's assertions, and the
// caller owns cancel(). Splitting the SETUP out rather than any of the
// assertions is deliberate: the assertions are what the test is for.
func startRotationFollower(t *testing.T, path string) (
	out, diag *syncBuffer,
	ctx context.Context, cancel context.CancelFunc,
	ticker *rotationFakeTicker, done chan error,
) {
	t.Helper()
	out = &syncBuffer{}
	diag = &syncBuffer{}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	ticker = newRotationFakeTicker()
	done = make(chan error, 1)
	go func() {
		done <- DaemonLogsHandler(ctx, DaemonLogsOptions{
			Path: path, Follow: true, Out: out, Diag: diag,
			PollInterval: time.Second, // irrelevant: Ticker paces the loop
			Ticker:       ticker,
		})
	}()
	return out, diag, ctx, cancel, ticker, done
}
