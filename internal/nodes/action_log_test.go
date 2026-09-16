package nodes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose (this file): the durable dedup guarantee, asserted across a
//   RESTART rather than within one process — an in-memory map would pass
//   every same-process assertion and lose the whole guarantee on a crash.
// SPORT: internal/nodes tests (ADD) — P1-E17-W4-S37-T2.

// TestAnActionIsReservedOnceWithinAProcess is the basic refusal.
func TestAnActionIsReservedOnceWithinAProcess(t *testing.T) {
	log := NewFileActionLog(t.TempDir())
	ctx := context.Background()

	if already, err := log.Reserve(ctx, "a1"); err != nil || already {
		t.Fatalf("first Reserve = (%v, %v), want (false, nil)", already, err)
	}
	already, err := log.Reserve(ctx, "a1")
	if err != nil {
		t.Fatalf("second Reserve: %v", err)
	}
	if !already {
		t.Fatal("the same action id was reserved twice")
	}
}

// TestAReservationSurvivesARestart is the assertion that matters. A
// redelivery arrives exactly in the window a crash opens, so a log that
// only remembers in memory is dedup that works when it is not needed and
// fails when it is.
func TestAReservationSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	if _, err := NewFileActionLog(dir).Reserve(ctx, "a1"); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	// A completely new log over the same directory is what the node has
	// after a restart.
	already, err := NewFileActionLog(dir).Reserve(ctx, "a1")
	if err != nil {
		t.Fatalf("Reserve after restart: %v", err)
	}
	if !already {
		t.Fatal("the reservation did not survive a restart; a redelivery would run the action again")
	}
}

// TestReserveAndTheAdmissionAgreeAcrossARestart drives the same thing
// through ReserveAction, which is the function the node actually calls, so
// the refusal it produces is the typed duplicate error.
func TestReserveAndTheAdmissionAgreeAcrossARestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	action := Action{ID: "a1"}

	if err := ReserveAction(ctx, NewFileActionLog(dir), action); err != nil {
		t.Fatalf("first admission: %v", err)
	}
	err := ReserveAction(ctx, NewFileActionLog(dir), action)
	if err == nil {
		t.Fatal("a redelivered action was admitted after a restart")
	}
	if !strings.Contains(err.Error(), "a1") {
		t.Errorf("error = %v, want it to name the duplicated action", err)
	}
}

// TestACompletedActionStaysReserved proves the outcome does not release
// the reservation: a redelivery arriving after the work finished must still
// be refused, or the side effect happens twice.
func TestACompletedActionStaysReserved(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	log := NewFileActionLog(dir)

	if _, err := log.Reserve(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if err := log.Complete(ctx, "a1", OutcomeSucceeded); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if already, err := log.Reserve(ctx, "a1"); err != nil || !already {
		t.Fatalf("Reserve after Complete = (%v, %v), want already=true", already, err)
	}
	if outcome, ok := log.Outcome("a1"); !ok || outcome != OutcomeSucceeded {
		t.Errorf("recorded outcome = %q (ok=%v), want succeeded", outcome, ok)
	}
}

// TestCompletingAnUnreservedActionIsRefused proves the log cannot record an
// outcome for work it never admitted, which would otherwise manufacture a
// reservation after the fact.
func TestCompletingAnUnreservedActionIsRefused(t *testing.T) {
	log := NewFileActionLog(t.TempDir())
	if err := log.Complete(context.Background(), "never-reserved", OutcomeSucceeded); err == nil {
		t.Fatal("an outcome was recorded for an action that was never reserved")
	}
}

// TestAnActionIDCannotEscapeTheLogDirectory proves an id carrying path
// separators addresses a file inside the log, not one outside it.
func TestAnActionIDCannotEscapeTheLogDirectory(t *testing.T) {
	dir := t.TempDir()
	log := NewFileActionLog(dir)

	if _, err := log.Reserve(context.Background(), "../../escaped"); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "..", "..", "escaped.json")); err == nil {
		t.Fatal("an action id escaped the log directory")
	}
	entries, err := os.ReadDir(filepath.Join(dir, "nodes", "actions"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("the reservation did not land inside the log: %v, %d entries", err, len(entries))
	}
}

// TestAnUnidentifiedActionIsRefused covers the empty id on both methods.
func TestAnUnidentifiedActionIsRefused(t *testing.T) {
	log := NewFileActionLog(t.TempDir())
	ctx := context.Background()
	if _, err := log.Reserve(ctx, ""); err == nil {
		t.Error("an action with no id was reserved")
	}
	if err := log.Complete(ctx, "", OutcomeSucceeded); err == nil {
		t.Error("an action with no id was completed")
	}
}
