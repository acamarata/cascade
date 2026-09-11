package jobs

import "testing"

func TestDecodeJobStateUnknownFailsClosed(t *testing.T) {
	for _, raw := range []string{"", "bogus", "PENDING", "leased "} {
		if got := DecodeJobState(raw); got != JobStateFailed {
			t.Errorf("DecodeJobState(%q) = %q, want failed", raw, got)
		}
	}
}

func TestDecodeJobStateKnownRoundTrips(t *testing.T) {
	for _, s := range []JobState{
		JobStatePending, JobStateLeased, JobStateRunning, JobStateVerifying,
		JobStateReviewing, JobStateAccepted, JobStateRejected,
		JobStateCancelling, JobStateCancelled, JobStateFailed,
	} {
		if got := DecodeJobState(string(s)); got != s {
			t.Errorf("DecodeJobState(%q) = %q, want %q", s, got, s)
		}
	}
}

func TestTransitionAllowedPublicEdges(t *testing.T) {
	cases := []struct {
		from, to JobState
	}{
		{JobStatePending, JobStateLeased},
		{JobStateLeased, JobStateRunning},
		{JobStatePending, JobStateCancelling},
		{JobStateRunning, JobStateCancelling},
		{JobStateCancelling, JobStateCancelled},
		{JobStateReviewing, JobStateFailed},
	}
	for _, c := range cases {
		if err := TransitionAllowed(c.from, c.to); err != nil {
			t.Errorf("TransitionAllowed(%s, %s) = %v, want nil", c.from, c.to, err)
		}
	}
}

// TestJobStateCancelling asserts the cancelling edges and that cancelled
// is reachable through no other path.
func TestJobStateCancelling(t *testing.T) {
	nonTerminal := []JobState{
		JobStatePending, JobStateLeased, JobStateRunning, JobStateVerifying,
		JobStateReviewing, JobStateCancelling,
	}
	for _, from := range nonTerminal {
		if err := TransitionAllowed(from, JobStateCancelling); err != nil {
			t.Errorf("TransitionAllowed(%s, cancelling) = %v, want nil", from, err)
		}
	}
	if err := TransitionAllowed(JobStateCancelling, JobStateCancelled); err != nil {
		t.Errorf("TransitionAllowed(cancelling, cancelled) = %v, want nil", err)
	}
	// cancelled is unreachable from anything except cancelling.
	for _, from := range []JobState{
		JobStatePending, JobStateLeased, JobStateRunning, JobStateVerifying,
		JobStateReviewing, JobStateAccepted, JobStateRejected, JobStateFailed,
	} {
		if err := TransitionAllowed(from, JobStateCancelled); err == nil {
			t.Errorf("TransitionAllowed(%s, cancelled) = nil, want error (only cancelling->cancelled is legal)", from)
		}
	}
}

func TestTransitionAllowedPolicyReservedRefused(t *testing.T) {
	cases := []struct{ from, to JobState }{
		{JobStateRunning, JobStateVerifying},
		{JobStateVerifying, JobStateReviewing},
		{JobStateReviewing, JobStateAccepted},
		{JobStateReviewing, JobStateRejected},
	}
	for _, c := range cases {
		if err := TransitionAllowed(c.from, c.to); err == nil {
			t.Errorf("TransitionAllowed(%s, %s) = nil, want typed refusal (policy-reserved)", c.from, c.to)
		}
	}
}

func TestTransitionAllowedIllegalRefused(t *testing.T) {
	if err := TransitionAllowed(JobStatePending, JobStateAccepted); err == nil {
		t.Error("TransitionAllowed(pending, accepted) = nil, want error")
	}
	if err := TransitionAllowed(JobStateAccepted, JobStatePending); err == nil {
		t.Error("TransitionAllowed(accepted, pending) = nil, want error (accepted is terminal)")
	}
}

func TestTransitionAllowedUnknownStateFailsClosed(t *testing.T) {
	if err := TransitionAllowed(JobState("bogus"), JobStateLeased); err == nil {
		t.Error("TransitionAllowed(bogus, leased) = nil, want error")
	}
	if err := TransitionAllowed(JobStatePending, JobState("bogus")); err == nil {
		t.Error("TransitionAllowed(pending, bogus) = nil, want error")
	}
}

func TestPolicyTransitionAllowed(t *testing.T) {
	if err := PolicyTransitionAllowed(JobStateRunning, JobStateVerifying); err != nil {
		t.Errorf("PolicyTransitionAllowed(running, verifying) = %v, want nil", err)
	}
	if err := PolicyTransitionAllowed(JobStatePending, JobStateLeased); err != nil {
		t.Errorf("PolicyTransitionAllowed(pending, leased) = %v, want nil (public edges also allowed)", err)
	}
	if err := PolicyTransitionAllowed(JobStatePending, JobStateAccepted); err == nil {
		t.Error("PolicyTransitionAllowed(pending, accepted) = nil, want error")
	}
}

// FuzzJobState proves DecodeJobState never panics on arbitrary input and
// always resolves unknown/unparseable values to JobStateFailed, never a
// zero value (06 §5.7).
func FuzzJobState(f *testing.F) {
	for _, seed := range []string{
		"", "pending", "leased", "running", "verifying", "reviewing",
		"accepted", "rejected", "cancelling", "cancelled", "failed",
		"PENDING", "bogus", "\x00\x01", "pending\x00",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		got := DecodeJobState(raw)
		if !got.Valid() {
			t.Fatalf("DecodeJobState(%q) = %q, not a valid JobState", raw, got)
		}
		if JobState(raw) != got && got != JobStateFailed {
			t.Fatalf("DecodeJobState(%q) = %q, want either %q itself (if valid) or failed", raw, got, raw)
		}
	})
}

func TestJobStateTerminal(t *testing.T) {
	terminal := map[JobState]bool{
		JobStateAccepted: true, JobStateRejected: true,
		JobStateCancelled: true, JobStateFailed: true,
		JobStatePending: false, JobStateLeased: false, JobStateRunning: false,
		JobStateVerifying: false, JobStateReviewing: false, JobStateCancelling: false,
	}
	for s, want := range terminal {
		if got := s.Terminal(); got != want {
			t.Errorf("%s.Terminal() = %v, want %v", s, got, want)
		}
	}
}

func TestPolicyTransitionAllowedUnknownStates(t *testing.T) {
	if err := PolicyTransitionAllowed(JobState("bogus"), JobStateLeased); err == nil {
		t.Error("PolicyTransitionAllowed(bogus, leased) = nil, want error")
	}
	if err := PolicyTransitionAllowed(JobStatePending, JobState("bogus")); err == nil {
		t.Error("PolicyTransitionAllowed(pending, bogus) = nil, want error")
	}
}
