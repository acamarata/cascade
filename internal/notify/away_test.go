package notify

import (
	"context"
	"testing"
	"time"
)

func TestAwayStateString(t *testing.T) {
	cases := map[AwayState]string{StateActive: "active", StateAwayPending: "away-pending", StateAway: "away", AwayState(99): "unknown"}
	for state, want := range cases {
		if got := state.String(); got != want {
			t.Errorf("AwayState(%d).String() = %q, want %q", state, got, want)
		}
	}
}

// TestAwayThresholdNotMetStaysActive is acceptance criterion 1's negative
// case: elapsed idle under the threshold keeps the controller Active.
func TestAwayThresholdNotMetStaysActive(t *testing.T) {
	h := newTestAway(t, newFixedClockPtr(time.Unix(1000, 0)), stubAdmission{}, "node-1")

	h.clock.Advance(5 * time.Minute) // threshold is 10m
	if got := h.ac.Tick(context.Background()); got != StateActive {
		t.Fatalf("Tick before threshold = %v, want StateActive", got)
	}
	if h.router.Accumulating() {
		t.Fatal("accumulation on while Active")
	}
}

// TestAwayFullTransitionActiveToAwayToActive drives the complete cycle and
// asserts the router's own accumulation flag at each step, so the test fails
// if the accumulate flip is ever not actually made.
func TestAwayFullTransitionActiveToAwayToActive(t *testing.T) {
	admission := &admissionBox{busy: true}
	h := newTestAway(t, newFixedClockPtr(time.Unix(2000, 0)), admission, "node-1")

	h.clock.Advance(11 * time.Minute)
	if got := h.ac.Tick(context.Background()); got != StateAwayPending {
		t.Fatalf("Tick after threshold = %v, want StateAwayPending", got)
	}
	if h.router.Accumulating() {
		t.Fatal("accumulation on during AwayPending, want not yet")
	}
	if got := h.ac.Tick(context.Background()); got != StateAwayPending {
		t.Fatalf("Tick with the machine side busy = %v, want StateAwayPending", got)
	}

	// The machine side goes idle; the confirming window must then hold for
	// the whole threshold before Away (the busy samples above restarted it).
	admission.setBusy(false)
	h.clock.Advance(11 * time.Minute)
	if got := h.ac.Tick(context.Background()); got != StateAway {
		t.Fatalf("Tick after a full idle window = %v, want StateAway", got)
	}
	if !h.router.Accumulating() {
		t.Fatal("accumulation NOT on after entering Away")
	}

	if got := h.ac.HandleEvent(context.Background(), ActivityEvent{At: h.clock.Now()}); got != StateActive {
		t.Fatalf("HandleEvent(activity) = %v, want StateActive", got)
	}
	if h.router.Accumulating() {
		t.Fatal("accumulation still on after returning to Active")
	}
}

// TestAwaySustainedNeedsBothSignalsForTheWholeWindow is the "sustained"
// requirement, which an instantaneous sample cannot satisfy. The refused
// input is the CR's: the machine side busy through the window and idle at
// the exact Tick instant must NOT produce Away.
func TestAwaySustainedNeedsBothSignalsForTheWholeWindow(t *testing.T) {
	admission := &admissionBox{}
	h := newTestAway(t, newFixedClockPtr(time.Unix(7000, 0)), admission, "node-1")
	ctx := context.Background()

	// Busy samples mid-window: presence has been idle the whole time, so the
	// controller reaches AwayPending, but the confirming window restarts at
	// every busy Tick and Away never comes.
	for i := 0; i < 6; i++ {
		admission.setBusy(true)
		h.clock.Advance(4 * time.Minute)
		if got := h.ac.Tick(ctx); got == StateAway {
			t.Fatalf("Tick %d entered Away with a busy machine side", i)
		}
	}
	if h.ac.State() != StateAwayPending {
		t.Fatalf("State() = %v, want StateAwayPending after a long presence-idle stretch", h.ac.State())
	}

	// Idle at this single Tick instant only: the window began at the last
	// busy sample 2m ago, so this is not a sustained window either.
	admission.setBusy(false)
	h.clock.Advance(2 * time.Minute)
	if got := h.ac.Tick(ctx); got != StateAwayPending {
		t.Fatalf("Tick idle at one instant = %v, want StateAwayPending (window restarted)", got)
	}
	if h.router.Accumulating() {
		t.Fatal("accumulation on without a sustained window")
	}

	// The window now holds for the whole threshold: Away.
	h.clock.Advance(11 * time.Minute)
	if got := h.ac.Tick(ctx); got != StateAway {
		t.Fatalf("Tick after a full both-idle window = %v, want StateAway", got)
	}
}

// TestAwayPresenceActivityAtTickReturnsToActive proves the injected
// PresenceSource really drives the return edge: no bus event, no event Kind,
// just a fresher operator-activity time observed at the next Tick.
func TestAwayPresenceActivityAtTickReturnsToActive(t *testing.T) {
	h := newTestAway(t, newFixedClockPtr(time.Unix(8000, 0)), stubAdmission{}, "node-1")
	h.driveToAway(t)

	h.presence.set(h.clock.Now())
	if got := h.ac.Tick(context.Background()); got != StateActive {
		t.Fatalf("Tick with fresh presence = %v, want StateActive", got)
	}
	if h.router.Accumulating() {
		t.Fatal("accumulation still on after a presence-driven return")
	}
	if h.ac.EpisodeID() != "" {
		t.Fatalf("EpisodeID() = %q after return, want empty", h.ac.EpisodeID())
	}
}

// TestAwayNilAdmissionNeverConfirmsAway is the fail-closed control: a
// controller with no machine-idle signal stays AwayPending forever rather
// than assuming idleness.
func TestAwayNilAdmissionNeverConfirmsAway(t *testing.T) {
	h := newAwayFixture(t, newFixedClockPtr(time.Unix(9000, 0)), nil, "node-1")
	h.deps.Admission = nil
	h.ac = NewAwayController(h.deps, h.cfg)

	for i := 0; i < 5; i++ {
		h.clock.Advance(11 * time.Minute)
		if got := h.ac.Tick(context.Background()); got == StateAway {
			t.Fatalf("Tick %d entered Away with no admission source wired", i)
		}
	}
	if h.router.Accumulating() {
		t.Fatal("accumulation on with no admission source wired")
	}
}
