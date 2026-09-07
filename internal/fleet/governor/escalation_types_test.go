package governor

// Purpose: EscalationRung's enum vocabulary (String, safeRung) and a
// round-trip sanity check on EscalationEvent/EscalationPolicy's plain
// data shape.

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEscalationSafeRungFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		in   EscalationRung
		want EscalationRung
	}{
		{"zero value", EscalationRung(0), RungHuman},
		{"negative", EscalationRung(-1), RungHuman},
		{"one past Human", RungHuman + 1, RungHuman},
		{"far beyond Human", EscalationRung(99), RungHuman},
		{"Retry unchanged", RungRetry, RungRetry},
		{"Context unchanged", RungContext, RungContext},
		{"SupervisorTask unchanged", RungSupervisorTask, RungSupervisorTask},
		{"Human unchanged", RungHuman, RungHuman},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := safeRung(tc.in); got != tc.want {
				t.Fatalf("safeRung(%d) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestEscalationSafeRungIsFixedPointAtHuman is the ladder's termination
// proof: RungHuman is the unique fixed point of "safeRung(r+1)", so
// repeatedly advancing from RungRetry reaches RungHuman in exactly three
// steps and can never step past it, no matter how many further advances
// are applied.
func TestEscalationSafeRungIsFixedPointAtHuman(t *testing.T) {
	if got := safeRung(RungHuman + 1); got != RungHuman {
		t.Fatalf("safeRung(RungHuman+1) = %v, want RungHuman (fixed point)", got)
	}
	r := RungRetry
	for i := 0; i < 10; i++ {
		r = safeRung(r + 1)
	}
	if r != RungHuman {
		t.Fatalf("repeated advance from RungRetry settled on %v after 10 steps, want RungHuman", r)
	}
}

func TestEscalationRungString(t *testing.T) {
	cases := []struct {
		r    EscalationRung
		want string
	}{
		{RungRetry, "retry"},
		{RungContext, "context"},
		{RungSupervisorTask, "supervisor-task"},
		{RungHuman, "human"},
		{EscalationRung(0), "invalid-rung"},
		{EscalationRung(99), "invalid-rung"},
	}
	for _, tc := range cases {
		if got := tc.r.String(); got != tc.want {
			t.Errorf("EscalationRung(%d).String() = %q, want %q", tc.r, got, tc.want)
		}
	}
}

func TestEscalationEventJSONRoundTrip(t *testing.T) {
	want := EscalationEvent{
		EntityID:  "task-1",
		Rung:      RungSupervisorTask,
		Attempt:   3,
		LastError: "boom",
		Timestamp: time.Unix(1000, 0).UTC(),
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got EscalationEvent
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got != want {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestEscalationPolicyMissingRungReadsAsZeroBudget(t *testing.T) {
	policy := EscalationPolicy{MaxAttempts: map[EscalationRung]int{RungRetry: 5}}
	if budget := policy.MaxAttempts[RungContext]; budget != 0 {
		t.Fatalf("unconfigured rung budget = %d, want 0 (fail-closed default)", budget)
	}
}
