//go:build !windows

package daemon

// Purpose: core unit tests for UpgradeManager's skew-detect, relaunch, and
// resume-cursor behavior (upgrade.go). Split from upgrade_conntracker_test.go
// (Drain/ConnTracker) and upgrade_run_test.go (Run-level wiring) purely to
// stay under the 300-line file cap — a files_scope deviation made for the
// same hard-gate reason upgrade_conntracker.go was: see the ticket report.
// Every clock wait uses runtime.FixedClock plus a Sleep stub that advances
// it, never a real sleep (Art.7.3); syscall.Exec is stubbed via execFunc so
// no test forks a real process. No "net"/"net/http" import (Art.7.2's
// no-network-unit-lane gate) — Drain/AttemptUpgrade take an io.Closer, so
// a plain fakeListener (upgrade_conntracker_test.go) stands in.
// SPORT: internal/daemon (ADD, per T-5 sport_updates).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// advancingSleep returns a Sleep func that advances clk instead of
// sleeping for real, the pattern every clock-bounded wait in this
// package's tests uses.
func advancingSleep(clk *runtime.FixedClock) func(time.Duration) {
	return func(d time.Duration) { clk.Advance(d) }
}

// newTestManager builds an UpgradeManager against a fixed, advanceable
// clock. store is typed *storetest.MemStore: passing a nil pointer
// straight into NewUpgradeManager's provider.Store parameter would wrap
// it as a non-nil interface holding a nil pointer, defeating
// UpgradeManager's own `m.Store == nil` checks, so a nil store keeps the
// interface itself nil.
func newTestManager(t *testing.T, store *storetest.MemStore, bus *events.Bus) (*UpgradeManager, *runtime.FixedClock) {
	t.Helper()
	clk := runtime.NewFixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	log, _ := newRecordingLogger()
	if store == nil {
		return NewUpgradeManager(clk, advancingSleep(clk), nil, bus, log), clk
	}
	return NewUpgradeManager(clk, advancingSleep(clk), store, bus, log), clk
}

func writeTempBinary(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "cascade-bin")
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatalf("write temp binary: %v", err)
	}
	return path
}

// TestSkewDetected: a modified on-disk binary reports skew against the
// (dev-default) embedded BuildHash.
func TestSkewDetected(t *testing.T) {
	// Stamp a digest so this exercises the real skew comparison. An
	// unstamped build now reports no skew by design, and without this
	// the test would pass only because the sentinel never equals a hash,
	// which is the bug that made dev builds relaunch on every shutdown.
	setBuildHash(t, "1111111111111111111111111111111111111111111111111111111111111111")
	m, _ := newTestManager(t, nil, nil)
	path := writeTempBinary(t, "not-the-build-hash-contents")

	skew, err := m.CheckSkew(path)
	if err != nil {
		t.Fatalf("CheckSkew: %v", err)
	}
	if !skew {
		t.Fatal("CheckSkew: want skew=true against an arbitrary on-disk binary")
	}
}

// TestNoSkewNoOp: a binary whose hash matches BuildHash() reports no
// skew, and AttemptUpgrade takes the no-op branch (no Drain, no Relaunch).
func TestNoSkewNoOp(t *testing.T) {
	origHash := buildHash
	origExec := execFunc
	t.Cleanup(func() { buildHash = origHash; execFunc = origExec })

	path := writeTempBinary(t, "matching-contents")
	sum, err := hashFile(path)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}
	buildHash = sum

	m, _ := newTestManager(t, nil, nil)
	skew, err := m.CheckSkew(path)
	if err != nil || skew {
		t.Fatalf("CheckSkew = %v, %v; want false, nil", skew, err)
	}

	execCalled := false
	execFunc = func(string, []string, []string) error { execCalled = true; return nil }
	relaunched, err := m.AttemptUpgrade(context.Background(), path, nil, nil, time.Second, nil, nil)
	if err != nil || relaunched {
		t.Fatalf("AttemptUpgrade = %v, %v; want false, nil", relaunched, err)
	}
	if execCalled {
		t.Fatal("AttemptUpgrade: execFunc must not be called on a no-skew no-op")
	}
}

// TestCheckSkew_UnreadableBinary surfaces the I/O failure as a typed
// cascade.KindUnavailable error rather than silently reporting no skew.
func TestCheckSkew_UnreadableBinary(t *testing.T) {
	// Stamp a real digest first. An unstamped build short-circuits before
	// the hash is attempted, which is correct but would take this test off
	// the path it exists to cover.
	setBuildHash(t, "0000000000000000000000000000000000000000000000000000000000000000")
	m, _ := newTestManager(t, nil, nil)
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	_, err := m.CheckSkew(missing)
	if err == nil {
		t.Fatal("CheckSkew: want error for a missing binary")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("CheckSkew error = %v; want KindUnavailable", err)
	}
}

// TestExecRelaunch: Relaunch calls the stubbed execFunc with the exact
// path/args/env it was given.
func TestExecRelaunch(t *testing.T) {
	orig := execFunc
	t.Cleanup(func() { execFunc = orig })

	var gotPath string
	var gotArgs, gotEnv []string
	execFunc = func(path string, args, env []string) error {
		gotPath, gotArgs, gotEnv = path, args, env
		return nil
	}

	m, _ := newTestManager(t, nil, nil)
	wantArgs := []string{"/bin/cascade", "daemon", "run"}
	wantEnv := []string{"FOO=bar"}
	if err := m.Relaunch("/bin/cascade", wantArgs, wantEnv); err != nil {
		t.Fatalf("Relaunch: %v", err)
	}
	if gotPath != "/bin/cascade" {
		t.Fatalf("execFunc path = %q", gotPath)
	}
	if len(gotArgs) != 3 || gotArgs[0] != "/bin/cascade" {
		t.Fatalf("execFunc args = %v", gotArgs)
	}
	if len(gotEnv) != 1 || gotEnv[0] != "FOO=bar" {
		t.Fatalf("execFunc env = %v", gotEnv)
	}
}

// TestExecRelaunch_Failure surfaces execFunc's error as a typed
// cascade.KindUnavailable error, leaving the caller (still running) to
// decide what happens next — Relaunch itself closes nothing.
func TestExecRelaunch_Failure(t *testing.T) {
	orig := execFunc
	t.Cleanup(func() { execFunc = orig })
	execFunc = func(string, []string, []string) error { return errors.New("permission denied") }

	m, _ := newTestManager(t, nil, nil)
	err := m.Relaunch("/bin/cascade", nil, nil)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Relaunch error = %v; want KindUnavailable", err)
	}
}

// TestResumeLegAbsent: no cursor written -> ReadResumeCursor reports
// found=false and no error, the ordinary "starting clean" case.
func TestResumeLegAbsent(t *testing.T) {
	store := storetest.NewMemStore()
	m, _ := newTestManager(t, store, nil)

	pos, found, err := m.ReadResumeCursor(context.Background())
	if err != nil || found || pos != "" {
		t.Fatalf("ReadResumeCursor = %q, %v, %v; want clean start", pos, found, err)
	}
}

// TestResumeLegAbsent_NilStore: a nil Store (the common case until a
// composition root wires one in) is the same clean-start no-op.
func TestResumeLegAbsent_NilStore(t *testing.T) {
	m, _ := newTestManager(t, nil, nil)
	if _, found, err := m.ReadResumeCursor(context.Background()); found || err != nil {
		t.Fatalf("ReadResumeCursor with nil Store: found=%v err=%v", found, err)
	}
	m.WriteResumeCursor(context.Background(), "wal-pos-123") // must not panic
}

// TestResumeLegPresent: a written cursor is read back verbatim.
func TestResumeLegPresent(t *testing.T) {
	store := storetest.NewMemStore()
	m, _ := newTestManager(t, store, nil)

	m.WriteResumeCursor(context.Background(), "wal-pos-42")
	pos, found, err := m.ReadResumeCursor(context.Background())
	if err != nil {
		t.Fatalf("ReadResumeCursor: %v", err)
	}
	if !found || pos != "wal-pos-42" {
		t.Fatalf("ReadResumeCursor = %q, %v; want wal-pos-42, true", pos, found)
	}
}

// failingStore wraps a MemStore and fails every Put, to prove a cursor
// write failure is logged and non-fatal rather than propagated.
type failingStore struct{ *storetest.MemStore }

func (failingStore) Put(context.Context, string, string, []byte) error {
	return cascade.New(cascade.KindUnavailable, "disk full")
}

// TestResumeCursor_WriteFailureNonFatal: WriteResumeCursor never panics
// or returns anything the caller could fail on when the store errors.
func TestResumeCursor_WriteFailureNonFatal(t *testing.T) {
	m, _ := newTestManager(t, nil, nil)
	m.Store = failingStore{storetest.NewMemStore()}
	m.WriteResumeCursor(context.Background(), "wal-pos-1") // must not panic
}

// TestAttemptUpgrade_NilUpgradeIsNoOp proves Run's attemptUpgrade helper
// preserves pre-upgrade behavior verbatim when Upgrade is unset. Passing a
// literal nil (rather than a constructed net.Listener) needs no "net"
// import here: attemptUpgrade never touches ln when Upgrade is nil.
func TestAttemptUpgrade_NilUpgradeIsNoOp(t *testing.T) {
	if attemptUpgrade(context.Background(), RunOptions{}, nil) {
		t.Fatal("attemptUpgrade with nil Upgrade: want false")
	}
}
