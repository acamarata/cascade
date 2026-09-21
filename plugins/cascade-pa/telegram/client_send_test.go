package telegram

// Purpose (this file): the OUTBOUND half of BotClient's tests — every call that
//   writes bytes crosses the content-bearing egress gate, the tier argument is
//   load-bearing, and what reaches the transport is the firewall's output.
//
// SPORT: plugins/cascade-pa/telegram client-send-tests/TEST
//   (P1-E23-W5-S48-T1).

import (
	"context"
	"strings"
	"testing"
	"time"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

func TestBotClient_SendMessage_UnconfiguredEgressRefuses(t *testing.T) {
	doer := &fakeDoer{}
	c := newTestClient(doer, nil, newMemState())
	err := c.SendMessage(context.Background(), 1, cascadepa.TierInternal, "hi")
	if err == nil {
		t.Fatal("SendMessage succeeded with no egress capability")
	}
	if !strings.Contains(err.Error(), ErrNoEgressCapability.Error()) {
		t.Fatalf("got %v, want the no-capability refusal", err)
	}
	if len(doer.methods()) != 0 {
		t.Fatalf("the transport was called %v, want no call at all", doer.methods())
	}
}

// TestBotClient_SendMessagePostsWhatTheFirewallReturned is the substitution
// proof: the bytes that reach the transport are the gate's OUTPUT, so a stored
// secret that reached a reply string is redacted on the wire. A Guard that
// ignored its content argument would post the secret and fail here.
func TestBotClient_SendMessagePostsWhatTheFirewallReturned(t *testing.T) {
	doer := &fakeDoer{}
	gate := &tierGate{}
	c := newTestClient(doer, gate, newMemState())
	if err := c.SendMessage(context.Background(), 5, cascadepa.TierInternal,
		"the value is "+secretValue); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	sent := lastSent(t, doer)
	if strings.Contains(sent, secretValue) {
		t.Fatalf("the stored secret reached the wire verbatim: %q", sent)
	}
	if !strings.Contains(sent, redactedValue) {
		t.Fatalf("posted %q, want the firewall's redacted output", sent)
	}
}

// TestBotClient_RestrictedTierIsRefusedOnTheSendPath proves the TIER argument
// is real: the bridge class admits internal and public only, and the refusal
// happens before anything is posted.
func TestBotClient_RestrictedTierIsRefusedOnTheSendPath(t *testing.T) {
	doer := &fakeDoer{}
	c := newTestClient(doer, &tierGate{}, newMemState())
	for _, tier := range []cascadepa.SensitivityTier{cascadepa.TierRestricted, cascadepa.TierLocalOnly} {
		if err := c.SendMessage(context.Background(), 5, tier, "exfiltration attempt"); err == nil {
			t.Fatalf("tier %q was admitted onto the bridge send path", tier)
		}
	}
	if len(doer.methods()) != 0 {
		t.Fatalf("the transport was called %v for a refused tier", doer.methods())
	}
}

// TestBotClient_AnswerCallbackQueryIsGated: the callback reply path crosses the
// same gate. An outbound byte is an outbound byte.
func TestBotClient_AnswerCallbackQueryIsGated(t *testing.T) {
	doer := &fakeDoer{}
	gate := &tierGate{}
	c := newTestClient(doer, gate, newMemState())
	if err := c.AnswerCallbackQuery(context.Background(), "cb-1", cascadepa.TierInternal,
		"secret "+secretValue); err != nil {
		t.Fatalf("AnswerCallbackQuery: %v", err)
	}
	if got := lastSent(t, doer); strings.Contains(got, secretValue) {
		t.Fatalf("the callback answer carried the stored secret: %q", got)
	}
	if err := c.AnswerCallbackQuery(context.Background(), "cb-2", cascadepa.TierRestricted, "no"); err == nil {
		t.Fatal("a restricted callback answer was admitted")
	}
	if got := doer.methods(); len(got) != 1 || got[0] != MethodAnswerCallbackQuery {
		t.Fatalf("transport saw %v, want exactly one answerCallbackQuery", got)
	}
}

func TestBotClient_RequestsTheTwoDispatchableUpdateKindsOnly(t *testing.T) {
	doer := &fakeDoer{}
	doer.push(mustReadTestdata(t, "getupdates_text.json"), nil)
	_ = collectPoll(t, newTestClient(doer, &tierGate{}, newMemState()), 1)
	params, ok := doer.calls[0].params.(getUpdatesParams)
	if !ok {
		t.Fatalf("first call params = %T, want getUpdatesParams", doer.calls[0].params)
	}
	want := []string{"message", "callback_query"}
	if strings.Join(params.AllowedUpdates, ",") != strings.Join(want, ",") {
		t.Fatalf("allowed_updates = %v, want %v", params.AllowedUpdates, want)
	}
	if params.Timeout != int(DefaultPollTimeout.Seconds()) {
		t.Fatalf("timeout = %d, want %d", params.Timeout, int(DefaultPollTimeout.Seconds()))
	}
}

func TestSleepOrDone(t *testing.T) {
	if !sleepOrDone(context.Background(), time.Millisecond) {
		t.Fatal("sleepOrDone reported an interrupted sleep on a live ctx")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sleepOrDone(ctx, time.Hour) {
		t.Fatal("sleepOrDone slept through a cancelled ctx")
	}
}

func TestNextBackoffCaps(t *testing.T) {
	if got := nextBackoff(pollBackoffFloor); got != 2*pollBackoffFloor {
		t.Fatalf("nextBackoff(%v) = %v", pollBackoffFloor, got)
	}
	if got := nextBackoff(pollBackoffCap); got != pollBackoffCap {
		t.Fatalf("nextBackoff(%v) = %v, want the cap", pollBackoffCap, got)
	}
}
