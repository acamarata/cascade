package background

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestRouteVerifyTailToBrief verifies exact joining and separation of attempt data.
func TestRouteVerifyTailToBrief(t *testing.T) {
	for _, tc := range []struct {
		name, base, tail, want string
	}{
		{"both", "base", "tail", "base\n\ntail"},
		{"base only", "base", "", "base"},
		{"tail only", "", "tail", "tail"},
		{"empty", "", "", ""},
		{"whitespace", " base\n", "\n tail ", " base\n\n\n\n tail "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, detail := range []string{"", "failure"} {
				calls := 0
				got, err := RunSwarmAttemptBrief(Attempt{N: 1, VerifyTail: tc.tail, Err: detail}, func() (string, error) {
					calls++
					return tc.base, nil
				})
				if err != nil || calls != 1 {
					t.Fatalf("error=%v, brief calls=%d", err, calls)
				}
				if got.NextBrief != tc.want || strings.Contains(got.NextBrief, "failure") {
					t.Errorf("NextBrief=%q, want %q", got.NextBrief, tc.want)
				}
				wantOutcome := Outcome{Status: "done"}
				wantEvents := []Event{{Stage: "author", Kind: "stage"}}
				if detail != "" {
					wantOutcome = Outcome{Status: "needs-owner", Detail: detail}
					wantEvents = append(wantEvents, Event{Stage: "escalate", Kind: "escalate"})
				}
				wantEvents = append(wantEvents, Event{Stage: "done", Kind: "done"})
				if got.Outcome != wantOutcome || !reflect.DeepEqual(got.Events, wantEvents) {
					t.Errorf("Outcome=%+v, Events=%+v; want %+v, %+v", got.Outcome, got.Events, wantOutcome, wantEvents)
				}
			}
		})
	}

	got, err := RunSwarmAttemptBrief(Attempt{N: 1}, nil)
	if !errors.Is(err, ErrInvalidAttempt) || !reflect.DeepEqual(got, SwarmResult{}) {
		t.Fatalf("nil brief: result=%+v, error=%v", got, err)
	}
	got, err = RunSwarmAttemptBrief(Attempt{N: 1}, func() (string, error) {
		return "partial", ErrBrief
	})
	if !errors.Is(err, ErrBrief) || !reflect.DeepEqual(got, SwarmResult{}) {
		t.Fatalf("failed brief: result=%+v, error=%v", got, err)
	}
}
