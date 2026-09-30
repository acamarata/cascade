// Purpose: waitTreeGone's contract, on every OS, with an injected round:
// it stops at the first empty round, refuses a tree that never empties
// within its attempts, and wraps a round error by identity.
// SPORT: internal.ci.waitTreeGone/TESTED.
package ci

import (
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestWaitTreeGone(t *testing.T) {
	errRound := errors.New("round failed")
	cases := []struct {
		name     string
		counts   []int
		roundErr error
		attempts int
		wantErr  error
		wantMsg  string
		wantCall int
	}{
		{name: "empties after three rounds", counts: []int{2, 1, 0}, attempts: 4, wantCall: 3},
		{name: "never empties", counts: []int{1, 1, 1, 1, 1}, attempts: 4, wantCall: 4,
			wantMsg: "ci: the timed-out step's process tree is still running"},
		{name: "round error", counts: []int{1}, roundErr: errRound, attempts: 4, wantCall: 1,
			wantErr: errRound, wantMsg: "ci: reaping the timed-out step's process tree"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			round := func() (int, error) {
				n := tc.counts[calls]
				calls++
				return n, tc.roundErr
			}
			err := waitTreeGone(round, tc.attempts)
			if calls != tc.wantCall {
				t.Errorf("round called %d times, want %d", calls, tc.wantCall)
			}
			checkTreeErr(t, err, tc.wantErr, tc.wantMsg)
		})
	}
}

// checkTreeErr asserts err is nil when wantMsg is empty; otherwise that it
// is KindUnavailable, carries wantMsg, and wraps want by identity.
func checkTreeErr(t *testing.T, err, want error, wantMsg string) {
	t.Helper()
	if wantMsg == "" {
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		return
	}
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce.Kind != cascade.KindUnavailable {
		t.Fatalf("err = %v, want a KindUnavailable cascade.Error", err)
	}
	if ce.Msg != wantMsg {
		t.Errorf("message = %q, want %q", ce.Msg, wantMsg)
	}
	if want != nil && !errors.Is(err, want) {
		t.Errorf("err = %v, want it to wrap %v by identity", err, want)
	}
}
