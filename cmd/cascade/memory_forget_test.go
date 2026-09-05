// Purpose: tests for `cascade memory forget`'s surface: the reason flag it
//
//	sends, and the trace table it prints. The verb destroys a user's own
//	record with no prompt anywhere in its path, so what it PRINTS is part
//	of the contract: a caller has to be able to read, from the output
//	alone, what was removed, what was kept on purpose, and what nothing
//	here can reach.
//
// SPORT: cmd.cascade.cmd.memory (ADD, per T-4 sport_updates).
package main

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/memory"
)

// TestForgetSendsTheReason proves the flag reaches the daemon rather than
// being accepted and dropped.
func TestForgetSendsTheReason(t *testing.T) {
	h := &memoryHarness{result: memory.ForgetResult{ID: "project/x", Forgotten: true}}
	if _, _, err := h.run(t, "forget", "project/x", "--reason", "no longer true"); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if len(h.calls) != 1 || h.calls[0].Method != memory.MethodForget {
		t.Fatalf("calls = %+v, want one memory.forget", h.calls)
	}
	params, ok := h.calls[0].Params.(memory.ForgetParams)
	if !ok {
		t.Fatalf("params are %T, want ForgetParams", h.calls[0].Params)
	}
	if params.Reason != "no longer true" || params.ID != "project/x" {
		t.Fatalf("params = %+v, want the address and the reason", params)
	}
}

// TestForgetPrintsEveryTrace is the honesty test for the CLI half. A
// destructive verb that printed only "forgot X" would let a user believe
// more was destroyed than was, so every place the daemon reported has to
// appear in the output with its disposition.
func TestForgetPrintsEveryTrace(t *testing.T) {
	h := &memoryHarness{result: memory.ForgetResult{
		ID: "project/x", Forgotten: true,
		Traces: []memory.ForgetTrace{
			{Place: "record file", Disposition: memory.ForgetRemoved, Detail: "unlinked"},
			{Place: "tombstone", Disposition: memory.ForgetRetained, Detail: "keeps the deletion durable"},
			{Place: "record bytes on disk", Disposition: memory.ForgetUnreachable,
				Detail: "not shredded"},
		},
	}}
	stdout, _, err := h.run(t, "forget", "project/x")
	if err != nil {
		t.Fatalf("forget: %v", err)
	}
	for _, want := range []string{
		"forgot project/x", "tombstoned", "record file", "removed", "tombstone", "retained",
		"record bytes on disk", "unreachable", "not shredded",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the output does not mention %q:\n%s", want, stdout)
		}
	}
}

// TestForgetPrintsAFailedBackupNote proves the one failure that does not
// fail the call is still said out loud.
func TestForgetPrintsAFailedBackupNote(t *testing.T) {
	h := &memoryHarness{result: memory.ForgetResult{
		ID: "project/x", Forgotten: true, EventError: "the bus is down",
	}}
	stdout, _, err := h.run(t, "forget", "project/x")
	if err != nil {
		t.Fatalf("forget: %v", err)
	}
	if !strings.Contains(stdout, "the backup lane was not told") ||
		!strings.Contains(stdout, "the bus is down") {
		t.Fatalf("the output hides a failed backup note:\n%s", stdout)
	}
}

// TestForgetPrintsAnAlreadyForgottenRecordDifferently keeps a repeat call
// distinguishable from a first one, which is the same reason the dry run
// prints its own sentence.
func TestForgetPrintsAnAlreadyForgottenRecordDifferently(t *testing.T) {
	h := &memoryHarness{result: memory.ForgetResult{ID: "project/x", AlreadyForgotten: true}}
	stdout, _, err := h.run(t, "forget", "project/x")
	if err != nil {
		t.Fatalf("forget: %v", err)
	}
	if !strings.Contains(stdout, "was already forgotten") {
		t.Fatalf("a repeat forget read like a first one:\n%s", stdout)
	}
}
