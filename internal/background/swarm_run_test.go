package background

import (
	"errors"
	"reflect"
	"testing"
)

// TestMapSwarmToGcflow verifies orchestration, mapping, and failure contracts.
func TestMapSwarmToGcflow(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    Attempt
		base string
		next string
	}{
		{"success", Attempt{N: 1}, "", ""},
		{"base", Attempt{N: 2}, "base", "base"},
		{"tail", Attempt{N: 1, VerifyTail: "tail"}, "", "tail"},
		{"joined", Attempt{N: 3, VerifyTail: "tail"}, "base", "base\n\ntail"},
		{"owner", Attempt{N: 1, VerifyTail: "tail", Err: "failure"}, "base", "base\n\ntail"},
		{"unchanged", Attempt{N: 1, VerifyTail: "\n tail\n", Err: "failure"}, "", "\n tail\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			got, err := RunSwarmAttempt(tc.a, func() (string, error) {
				calls++
				return tc.base, nil
			})
			if err != nil || calls != 1 {
				t.Fatalf("error = %v, brief calls = %d", err, calls)
			}
			author, outcome := MapAttempt(tc.a)
			wantOutcome := Outcome{Status: "done"}
			events := []Event{{Stage: "author", Kind: "stage"}}
			if tc.a.Err != "" {
				wantOutcome = Outcome{Status: "needs-owner", Detail: tc.a.Err}
				events = append(events, Event{Stage: "escalate", Kind: "escalate"})
			}
			if author != events[0] || outcome != wantOutcome {
				t.Fatalf("mapping = %+v, %+v", author, outcome)
			}
			events = append(events, Event{Stage: "done", Kind: "done"})
			want := SwarmResult{Events: events, Outcome: wantOutcome, NextBrief: tc.next}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("result = %+v, want %+v", got, want)
			}
		})
	}
}

// TestRunSwarmAttemptInvalid verifies invalid attempts, nil briefs and brief failures.
func TestRunSwarmAttemptInvalid(t *testing.T) {
	calls := 0
	brief := func() (string, error) { calls++; return "base", nil }
	for _, n := range []int{0, -1} {
		got, err := RunSwarmAttempt(Attempt{N: n}, brief)
		if !errors.Is(err, ErrInvalidAttempt) || !reflect.DeepEqual(got, SwarmResult{}) || calls != 0 {
			t.Fatalf("invalid N=%d: result=%+v, error=%v, calls=%d", n, got, err, calls)
		}
	}
	got, err := RunSwarmAttempt(Attempt{N: 1}, nil)
	if !errors.Is(err, ErrInvalidAttempt) || !reflect.DeepEqual(got, SwarmResult{}) {
		t.Fatalf("nil brief: result=%+v, error=%v", got, err)
	}
	cause := errors.New("brief unavailable")
	got, err = RunSwarmAttempt(Attempt{N: 1, Err: "failure"}, func() (string, error) {
		calls++
		return "partial", cause
	})
	if calls != 1 || !errors.Is(err, ErrBrief) || !errors.Is(err, cause) || !reflect.DeepEqual(got, SwarmResult{}) {
		t.Fatalf("brief failure: result=%+v, error=%v, calls=%d", got, err, calls)
	}
}
