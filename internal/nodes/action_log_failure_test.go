package nodes

// Purpose (this file): the action log's FAILURE paths — what a node does
//   when the durable dedup record cannot be written.
// WHY IT MATTERS: the reservation is what stops a redelivered dispatch from
//   producing a second external side effect. A node that cannot write the
//   record and proceeds anyway has silently turned dedup off, which is the
//   one outcome R-21.221 does not permit. Every path here must REFUSE.
// Constraints: POSIX permission bits; skipped on windows (no equivalent)
//   and under a uid that ignores them.
// SPORT: internal/nodes tests (ADD) — P1-E17-W4-S37-T2.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// unwritableDir returns a directory that exists and cannot be written to.
func unwritableDir(t *testing.T) string {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not apply on this platform")
	}
	dir := filepath.Join(t.TempDir(), "log")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if f, err := os.CreateTemp(dir, "probe-"); err == nil {
		_ = f.Close()
		t.Skip("this uid ignores directory permissions; the refusal cannot be provoked here")
	}
	return dir
}

// actionLogDir is where NewFileActionLog puts reservations under dataDir.
// Spelled once here so a layout change breaks one line rather than three.
func actionLogDir(dataDir string) string {
	return filepath.Join(dataDir, "nodes", "actions")
}

// TestAnUnwritableActionLogRefusesRatherThanRunning is the rule. A node that
// cannot record the reservation must not execute the action: dedup that
// fails open is dedup that is off.
func TestAnUnwritableActionLogRefusesRatherThanRunning(t *testing.T) {
	log := NewFileActionLog(unwritableDir(t))

	already, err := log.Reserve(context.Background(), "a1")
	if err == nil {
		t.Fatal("reserving into an unwritable log reported success")
	}
	if already {
		t.Error("a failed reservation reported the action as already done, which would SKIP it")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Errorf("kind = %v (typed=%v), want %v", kind, ok, cascade.KindUnavailable)
	}
}

// TestReservingUnderAPathThatIsAFileIsRefused covers the other half of the
// open: the log directory cannot be created at all.
func TestReservingUnderAPathThatIsAFileIsRefused(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(blocker, []byte("not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileActionLog(blocker).Reserve(context.Background(), "a1"); err == nil {
		t.Fatal("reserving under a path that is a file reported success")
	}
}

// TestCompletingIntoAnUnwritableLogIsRefused covers the outcome half. A
// completion that silently vanishes leaves a reserved action with no
// terminal state, which is exactly the ambiguous outcome the held state
// exists for — so it must be reported, not swallowed.
func TestCompletingIntoAnUnwritableLogIsRefused(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not apply on this platform")
	}
	dataDir := t.TempDir()
	log := NewFileActionLog(dataDir)
	if _, err := log.Reserve(context.Background(), "a1"); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	// The log lives in a subdirectory of dataDir; the reservations are what
	// must become unwritable, not the root the caller passed.
	actions := actionLogDir(dataDir)
	if err := os.Chmod(actions, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(actions, 0o700) })
	if f, err := os.CreateTemp(actions, "probe-"); err == nil {
		_ = f.Close()
		t.Skip("this uid ignores directory permissions; the refusal cannot be provoked here")
	}

	if err := log.Complete(context.Background(), "a1", OutcomeSucceeded); err == nil {
		t.Fatal("completing into an unwritable log reported success")
	}
}

// TestAFailedWriteIsReportedNotSilentlyTruncated drives the encode/write/
// sync leg directly, with a handle that cannot be written to.
//
// Directly, because the only way this fires through Reserve is a disk that
// fills between the create and the write — real, and not something a test
// can arrange. The record is the dedup fact; a half-written one that
// reported success would read back as a reservation that does not decode.
func TestAFailedWriteIsReportedNotSilentlyTruncated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path) // read-only handle
	if err != nil {
		t.Fatal(err)
	}

	if err := writeAndSync(f, actionState{ActionID: "a1"}); err == nil {
		t.Fatal("writing through a read-only handle reported success")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("a failed write left %d byte(s) behind: %q", len(got), got)
	}
}

// TestAReservationIsValidJSONOnDisk pins the format the restart path reads
// back. It is the positive counterpart to the write failure above: a record
// nothing can decode is a reservation that stops deduplicating after the
// next restart, silently.
func TestAReservationIsValidJSONOnDisk(t *testing.T) {
	dataDir := t.TempDir()
	if _, err := NewFileActionLog(dataDir).Reserve(context.Background(), "a/1"); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	dir := actionLogDir(dataDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d file(s) in the log, want exactly 1", len(entries))
	}
	raw, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var state actionState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("the reservation on disk does not decode: %v (%q)", err, raw)
	}
	if state.ActionID != "a/1" {
		t.Errorf("action id = %q, want the id as given; the file NAME is encoded, the record is not",
			state.ActionID)
	}
}
