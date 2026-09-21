package telegram

// Purpose (this file): BotClient's tests — the long-poll loop, the durable
//   replay guard, the terminal/transient split, and the content-bearing
//   egress gate on every outbound call.
//
// SPORT: plugins/cascade-pa/telegram client-tests/TEST (P1-E23-W5-S48-T1).

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"

	"github.com/acamarata/cascade/pkg/cascade"
)

// newTestClient builds a client over the given state with an instant sleeper.
func newTestClient(doer Doer, gate EgressGate, state cascadepa.BridgeState) *BotClient {
	c := NewBotClient(testSubject, doer, gate, cascadepa.NewUpdateLedger(state))
	sleep, _ := instantSleep()
	c.sleep = sleep
	return c
}

// collectPoll runs Poll until it has seen want updates, then cancels.
func collectPoll(t *testing.T, c *BotClient, want int) []Update {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var (
		mu   sync.Mutex
		got  []Update
		done = make(chan error, 1)
	)
	go func() {
		done <- c.Poll(ctx, func(_ context.Context, u Update) {
			mu.Lock()
			got = append(got, u)
			n := len(got)
			mu.Unlock()
			if n >= want {
				cancel()
			}
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Poll returned %v, want nil after cancellation", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Poll did not return after the wanted updates arrived")
	}
	mu.Lock()
	defer mu.Unlock()
	return got
}

// pollBriefly runs Poll under a short deadline and returns every update it
// delivered — the shape a test needs when it expects NONE.
func pollBriefly(t *testing.T, c *BotClient) []Update {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	var got []Update
	if err := c.Poll(ctx, func(_ context.Context, u Update) { got = append(got, u) }); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	return got
}

func TestBotClient_PollDecodesFixtureAndDispatches(t *testing.T) {
	doer := &fakeDoer{}
	doer.push(mustReadTestdata(t, "getupdates_text.json"), nil)
	gate := &tierGate{}
	got := collectPoll(t, newTestClient(doer, gate, newMemState()), 1)
	if len(got) != 1 {
		t.Fatalf("got %d updates, want 1", len(got))
	}
	if got[0].UpdateID != 900000001 {
		t.Fatalf("UpdateID = %d, want 900000001", got[0].UpdateID)
	}
	if got[0].Message == nil || got[0].Message.Text != "hello from the bridge fixture" {
		t.Fatalf("Message = %+v", got[0].Message)
	}
	if len(gate.observedTiers()) == 0 {
		t.Fatal("the egress gate was never consulted before the transport was used")
	}
}

// TestBotClient_ReplayedUpdateIsProcessedOnce is the dedup proof. The same
// batch is returned twice, exactly as Telegram redelivers an unacknowledged
// update: the handler must run once.
func TestBotClient_ReplayedUpdateIsProcessedOnce(t *testing.T) {
	raw := mustReadTestdata(t, "getupdates_text.json")
	doer := &fakeDoer{}
	doer.push(raw, nil)
	doer.push(raw, nil)
	state := newMemState()
	c := newTestClient(doer, &tierGate{}, state)

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	var calls int
	if err := c.Poll(ctx, func(context.Context, Update) { calls++ }); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if calls != 1 {
		t.Fatalf("the handler ran %d times for one update id, want 1", calls)
	}
}

// TestBotClient_OffsetResumesAfterRestart proves the offset is durable: a
// SECOND client over the same state never re-delivers an update the first one
// already processed, which is what an in-memory offset reset to 0 did.
func TestBotClient_OffsetResumesAfterRestart(t *testing.T) {
	state := newMemState()
	first := &fakeDoer{}
	first.push(mustReadTestdata(t, "getupdates_text.json"), nil)
	if got := collectPoll(t, newTestClient(first, &tierGate{}, state), 1); len(got) != 1 {
		t.Fatalf("first client delivered %d updates, want 1", len(got))
	}
	offset, err := cascadepa.NewUpdateLedger(state).Offset(context.Background(), testSubject)
	if err != nil {
		t.Fatalf("Offset: %v", err)
	}
	if offset != 900000002 {
		t.Fatalf("persisted offset = %d, want 900000002", offset)
	}
	// The restarted client is handed the SAME batch again.
	second := &fakeDoer{}
	second.push(mustReadTestdata(t, "getupdates_text.json"), nil)
	if got := pollBriefly(t, newTestClient(second, &tierGate{}, state)); len(got) != 0 {
		t.Fatalf("the restarted client re-delivered %d updates, want 0", len(got))
	}
	if second.calls[0].params.(getUpdatesParams).Offset != 900000002 {
		t.Fatalf("the restarted client requested offset %d, want the persisted 900000002",
			second.calls[0].params.(getUpdatesParams).Offset)
	}
}

// TestBotClient_UnreadableLedgerDropsTheUpdate: a store this client cannot
// read must drop the update, never deliver it. Processing an update whose
// dedup state is unknown is how a replay reaches a handler.
func TestBotClient_UnreadableLedgerDropsTheUpdate(t *testing.T) {
	doer := &fakeDoer{}
	doer.push(mustReadTestdata(t, "getupdates_text.json"), nil)
	state := newMemState()
	state.loadErr = errors.New("store unreachable")
	if got := pollBriefly(t, newTestClient(doer, &tierGate{}, state)); len(got) != 0 {
		t.Fatalf("delivered %d updates with an unreadable ledger, want 0", len(got))
	}
}

// TestBotClient_UnconfiguredEgressNeverDials proves the gate is load-bearing
// on the INBOUND path too: with no capability wired the transport is never
// touched at all.
func TestBotClient_UnconfiguredEgressNeverDials(t *testing.T) {
	doer := &fakeDoer{}
	c := newTestClient(doer, nil, newMemState()) // nil -> unconfiguredEgressGate
	if got := pollBriefly(t, c); len(got) != 0 {
		t.Fatalf("delivered %d updates with no egress capability, want 0", len(got))
	}
	if len(doer.methods()) != 0 {
		t.Fatalf("the transport was called %v with no egress capability wired", doer.methods())
	}
}

// TestBotClient_Poll401StopsInsteadOfSpinning: a revoked token is terminal. The
// earlier draft treated it as transient and polled forever.
func TestBotClient_Poll401StopsInsteadOfSpinning(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want *cascade.Error
		kind cascade.Kind
	}{
		{"revoked token", `{"ok":false,"error_code":401,"description":"Unauthorized"}`,
			errTokenRejected, cascade.KindPolicyDenied},
		{"another poller", `{"ok":false,"error_code":409,"description":"Conflict"}`,
			errPollConflict, cascade.KindConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doer := &fakeDoer{}
			doer.push([]byte(tc.raw), nil)
			c := newTestClient(doer, &tierGate{}, newMemState())
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := c.Poll(ctx, func(context.Context, Update) { t.Fatal("an update was delivered") })
			if err == nil {
				t.Fatal("Poll returned nil, want the terminal refusal")
			}
			if !strings.Contains(err.Error(), tc.want.Error()) {
				t.Fatalf("Poll returned %v, want %v", err, tc.want)
			}
			if !cascade.HasKind(err, tc.kind) {
				t.Fatalf("Poll error kind is wrong: %v", err)
			}
			if len(doer.methods()) != 1 {
				t.Fatalf("the loop retried a terminal error %d times", len(doer.methods())-1)
			}
		})
	}
}

// TestBotClient_BackoffDoublesOnTransientError proves a transient failure is
// still retried, so the terminal test above is not simply "everything stops".
func TestBotClient_BackoffDoublesOnTransientError(t *testing.T) {
	doer := &fakeDoer{}
	doer.push(nil, errors.New("transient"))
	doer.push(nil, errors.New("transient"))
	doer.push(mustReadTestdata(t, "getupdates_text.json"), nil)
	c := NewBotClient(testSubject, doer, &tierGate{}, cascadepa.NewUpdateLedger(newMemState()))
	sleep, durations := instantSleep()
	c.sleep = sleep
	if got := collectPoll(t, c, 1); len(got) != 1 {
		t.Fatalf("got %d updates, want 1", len(got))
	}
	if len(*durations) != 2 {
		t.Fatalf("recorded %d backoffs, want 2: %v", len(*durations), *durations)
	}
	if (*durations)[0] != pollBackoffFloor || (*durations)[1] != pollBackoffFloor*2 {
		t.Fatalf("backoffs = %v, want [%v %v]", *durations, pollBackoffFloor, pollBackoffFloor*2)
	}
}

func TestBotClient_PollReturnsPromptlyOnCtxCancel(t *testing.T) {
	c := newTestClient(&fakeDoer{}, &tierGate{}, newMemState())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- c.Poll(ctx, func(context.Context, Update) {}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Poll on an already-cancelled ctx: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Poll did not return after cancellation")
	}
}

// emptySuccessDoer always answers with zero updates: the loop never sleeps, so
// this is the only path that proves the ctx check is independent of the
// backoff primitive's own cancellation awareness.
type emptySuccessDoer struct{}

func (emptySuccessDoer) Do(_ context.Context, _ string, _, out any) error {
	if list, ok := out.(*[]Update); ok {
		*list = nil
	}
	return nil
}

func TestBotClient_PollChecksCtxOnAnUnbrokenSuccessStreak(t *testing.T) {
	c := newTestClient(emptySuccessDoer{}, &tierGate{}, newMemState())
	if got := pollBriefly(t, c); len(got) != 0 {
		t.Fatalf("got %d updates from an empty stream", len(got))
	}
}
