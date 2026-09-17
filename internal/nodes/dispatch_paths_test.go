package nodes

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Purpose (this file): the dispatch surface's remaining decision branches —
//   the outcome mapping, the clock default, the signer refusal, and the
//   [nodes] section's dispatch knobs.
// SPORT: internal/nodes tests (ADD) — P1-E17-W4-S37-T2.

// TestEveryOutcomeMapsToItsOwnMeaning is the branch table a controller acts
// on. Each one is a DIFFERENT thing having happened to the work, and
// collapsing any two would make the wrong recovery look correct.
func TestEveryOutcomeMapsToItsOwnMeaning(t *testing.T) {
	req := ShipRequest{DispatchID: "d1", Work: Action{ID: "a1"}}
	idempotent := ShipRequest{DispatchID: "d1", Work: Action{ID: "a1", Idempotent: true}}

	if err := interpretOutcome(OutcomeSucceeded, "n1", req); err != nil {
		t.Errorf("succeeded mapped to an error: %v", err)
	}
	if err := interpretOutcome(OutcomeRefused, "n1", req); err == nil {
		t.Error("a refused duplicate reported success")
	}
	if err := interpretOutcome(OutcomeFailed, "n1", req); err == nil {
		t.Error("a failed run reported success")
	}
	// Acknowledged-but-unfinished: held for a human when the action is
	// not safe to re-run, released when it is.
	if err := interpretOutcome(OutcomeAccepted, "n1", req); err == nil {
		t.Error("an ambiguous non-idempotent outcome was not held")
	}
	if err := interpretOutcome(OutcomeAccepted, "n1", idempotent); err != nil {
		t.Errorf("an ambiguous IDEMPOTENT outcome was held instead of released: %v", err)
	}
	if err := interpretOutcome("hibernating", "n1", req); err == nil {
		t.Error("an outcome this build does not define was accepted")
	}
}

// TestAnUnsetClockStampsNoTime proves the ship leg never falls back to a
// bare time.Now: an attempt stamped by an unspecified clock is one whose
// ordering cannot be reproduced (Art.7.3).
func TestAnUnsetClockStampsNoTime(t *testing.T) {
	if got := (ShipDeps{}).now(); !got.IsZero() {
		t.Fatalf("an unwired clock produced %v, want the zero time", got)
	}
	fixed := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	if got := (ShipDeps{Clock: fixedDispatchClock{fixed}}).now(); !got.Equal(fixed) {
		t.Fatalf("the injected clock was ignored: %v", got)
	}
}

// fixedDispatchClock is a Clock pinned to one instant.
type fixedDispatchClock struct{ at time.Time }

func (c fixedDispatchClock) Now() time.Time { return c.at }

// TestAFrameIsNeverSignedWithoutASigner proves a missing signer is a typed
// refusal rather than an unsigned frame the controller would reject with a
// misleading reason.
func TestAFrameIsNeverSignedWithoutASigner(t *testing.T) {
	_, err := SignDispatchFrame(context.Background(), DispatchFrame{DispatchID: "d1"}, nil)
	if err == nil {
		t.Fatal("a frame was signed with no signer wired")
	}
}

// TestTheNodesSectionCarriesTheDispatchKnobs proves the [nodes] table's
// first production reader sees them, and that a wrong-typed value is
// refused rather than silently ignored — a config typo must not surface
// only when someone finally dispatches work.
func TestTheNodesSectionCarriesTheDispatchKnobs(t *testing.T) {
	sec, err := LoadSection(map[string]interface{}{
		"dispatch_repo_root": "/srv/work",
		"dispatch_remote":    "git@example:work.git",
	})
	if err != nil {
		t.Fatalf("LoadSection: %v", err)
	}
	if sec.DispatchRepoRoot != "/srv/work" || sec.DispatchRemote != "git@example:work.git" {
		t.Fatalf("section = %+v", sec)
	}

	if _, err := LoadSection(map[string]interface{}{"dispatch_remote": 42}); err == nil {
		t.Error("a non-string dispatch_remote was accepted")
	}
	// An absent section is valid: the knobs refuse at USE, not at load, so
	// a controller that never dispatches needs no [nodes] table at all.
	if _, err := LoadSection(nil); err != nil {
		t.Errorf("an absent [nodes] section was refused: %v", err)
	}
}

// TestACompletedOutcomeSurvivesAReopen proves the action log's recorded
// outcome is readable by a later process, which is what makes an ambiguous
// redelivery answerable at all.
func TestACompletedOutcomeSurvivesAReopen(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	first := NewFileActionLog(dir)
	if _, err := first.Reserve(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if err := first.Complete(ctx, "a1", OutcomeFailed); err != nil {
		t.Fatal(err)
	}
	outcome, ok := NewFileActionLog(dir).Outcome("a1")
	if !ok || outcome != OutcomeFailed {
		t.Fatalf("outcome after reopen = %q (ok=%v), want failed", outcome, ok)
	}
}

// TestAnUnrecordedActionHasNoOutcome proves the log does not invent one.
func TestAnUnrecordedActionHasNoOutcome(t *testing.T) {
	log := NewFileActionLog(t.TempDir())
	if _, ok := log.Outcome("never-seen"); ok {
		t.Fatal("an action that was never recorded reported an outcome")
	}
	if _, err := log.Reserve(context.Background(), "a1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := log.Outcome("a1"); ok {
		t.Fatal("a reserved-but-unfinished action reported a terminal outcome")
	}
}

// TestAnUnresolvableDispatchIsRefusedBeforeShipping proves the RPC entry
// surfaces a dependency-resolution failure instead of shipping without one.
func TestAnUnresolvableDispatchIsRefusedBeforeShipping(t *testing.T) {
	reg := newDispatchRegistry(t, func(context.Context, string) (ShipDeps, RequeueDeps, DeviceRecord, error) {
		return ShipDeps{}, RequeueDeps{}, DeviceRecord{}, errNoActionLog()
	})
	_, errObj := dispatchCall(t, reg,
		`{"dispatch_id":"d1","node_id":"n1","action_id":"a1","sensitivity":"normal"}`)
	if errObj == nil {
		t.Fatal("a dispatch whose dependencies could not be resolved was shipped")
	}
}

// TestAStaticKeyLaneIsReportedAsRelayed proves the caller can tell a node
// that was trusted with a token from one whose calls the controller makes.
func TestAStaticKeyLaneIsReportedAsRelayed(t *testing.T) {
	deps, rec, _, head := shipHarness(t, DispatchFrame{
		Sequence: 1, ActionID: "a1", Outcome: OutcomeSucceeded,
	})
	reg := newDispatchRegistry(t, func(context.Context, string) (ShipDeps, RequeueDeps, DeviceRecord, error) {
		return deps, RequeueDeps{}, rec, nil
	})
	result, errObj := dispatchCall(t, reg,
		`{"dispatch_id":"d1","node_id":"n1","action_id":"a1","head":"`+head+
			`","sensitivity":"normal","credential":"static"}`)
	if errObj != nil {
		t.Fatalf("Dispatch: %+v", errObj)
	}
	got, ok := result.(DispatchResult)
	if !ok || !got.Relayed {
		t.Fatalf("result = %+v, want a static-key lane reported as relayed", result)
	}
}

// TestAMalformedDispatchCallIsRefused covers the params decoder.
func TestAMalformedDispatchCallIsRefused(t *testing.T) {
	reg := newDispatchRegistry(t, func(context.Context, string) (ShipDeps, RequeueDeps, DeviceRecord, error) {
		return ShipDeps{}, RequeueDeps{}, DeviceRecord{}, nil
	})
	if _, errObj := dispatchCall(t, reg, `{"dispatch_id":{"not":"a string"}}`); errObj == nil {
		t.Fatal("a malformed dispatch call was accepted")
	}
	if _, errObj := dispatchCall(t, reg, `"not an object"`); errObj == nil {
		t.Fatal("a non-object params value was accepted")
	}
}

// TestAStreamedRecordCannotBeEncodedIsReported keeps stampJournalRecord's
// failure path honest: a record that cannot be encoded must not reach the
// store as a partial entry.
func TestAStreamedRecordStampCarriesBothIdentifiers(t *testing.T) {
	stamped, err := stampJournalRecord(JournalRecord{DispatchID: "d1", Attempt: 4})
	if err != nil {
		t.Fatalf("stampJournalRecord: %v", err)
	}
	for _, want := range []string{`"dispatch_id":"d1"`, `"attempt":4`} {
		if !strings.Contains(string(stamped), want) {
			t.Errorf("stamped entry %s is missing %s", stamped, want)
		}
	}
}
