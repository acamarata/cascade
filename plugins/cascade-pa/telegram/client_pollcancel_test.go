package telegram

// Purpose (this file): a deterministic test for BotClient.Poll's cancellation
//   branch after a failed fetch. The branch is otherwise reached only when a
//   cancel races a fetch, so coverage of it depended on scheduling.
//
// SPORT: plugins/cascade-pa/telegram client-tests/TEST (P1-CPA-19).

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// cancelThenFailDoer cancels the poll context inside the call and then
// returns a transient error, so cancellation is ordered by the doer itself.
type cancelThenFailDoer struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	calls  int
}

func (d *cancelThenFailDoer) Do(_ context.Context, _ string, _, _ any) error {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	d.cancel()
	return errors.New("transient transport failure after cancellation")
}

func (d *cancelThenFailDoer) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// TestPollReturnsNilWhenCancelledDuringFetch proves a cancel that lands
// during a failing fetch ends Poll cleanly: nil, one fetch, and no backoff.
func TestPollReturnsNilWhenCancelledDuringFetch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doer := &cancelThenFailDoer{cancel: cancel}
	c := newTestClient(doer, &tierGate{}, newMemState())
	sleep, durations := instantSleep()
	c.sleep = sleep

	err := c.Poll(ctx, func(context.Context, Update) {
		t.Error("onUpdate must not run: the only fetch failed")
	})

	if err != nil {
		t.Fatalf("Poll returned %v, want nil after cancellation during a fetch", err)
	}
	if got := doer.callCount(); got != 1 {
		t.Fatalf("doer ran %d times, want exactly 1", got)
	}
	if n := len(*durations); n != 0 {
		t.Fatalf("sleep was called %d times (%v), want 0: cancellation must skip backoff", n, *durations)
	}
}
