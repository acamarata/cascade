package telegram

// Purpose (this file): the owner-guard fakes every send test arms a BotClient
//   with, and the guard's own unit tests (outbound_guard.go).
//
// SPORT: plugins/cascade-pa/telegram owner-guard/TEST (P1-CPA-17).

import (
	"context"
	"strings"
	"testing"
	"time"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"

	"github.com/acamarata/cascade/pkg/cascade"
)

// positiveChats is an OwnerChatSource that admits every positive chat id, the
// shape of a private chat. Group and channel ids are negative and refuse.
type positiveChats struct{}

func (positiveChats) AllowsChat(_ context.Context, chatID int64) (bool, error) {
	return chatID > 0, nil
}

// failingChats is an OwnerChatSource whose store is down.
type failingChats struct{}

func (failingChats) AllowsChat(context.Context, int64) (bool, error) { return true, errTestStoreDown }

// recordingRefusals captures every bridge.refused event.
type recordingRefusals struct{ events []RefusalEvent }

func (r *recordingRefusals) EmitRefused(_ context.Context, e RefusalEvent) {
	r.events = append(r.events, e)
}

// armTestGuard arms c with positiveChats and a recording sink pinned to t0.
func armTestGuard(t *testing.T, c *BotClient) *recordingRefusals {
	t.Helper()
	sink := &recordingRefusals{}
	if err := c.SetOwnerGuard(positiveChats{}, sink, t0); err != nil {
		t.Fatalf("SetOwnerGuard: %v", err)
	}
	return sink
}

// requireNoCalls fails when the recording transport saw any Bot API call.
func requireNoCalls(t *testing.T, doer *fakeDoer) {
	t.Helper()
	if calls := doer.methods(); len(calls) != 0 {
		t.Fatalf("the bridge made Bot API calls where none are allowed: %v", calls)
	}
}

// assertChatRefused fails unless err is the guard's typed refusal.
func assertChatRefused(t *testing.T, err error) {
	t.Helper()
	if err != errChatNotPaired {
		t.Fatalf("err = %v, want the guard's refusal %q by identity", err, errChatNotPaired)
	}
	if !cascade.HasKind(err, cascade.KindPolicyDenied) || !strings.Contains(err.Error(), "chat is not the paired owner chat") {
		t.Fatalf("err = %v, want KindPolicyDenied naming the owner chat", err)
	}
}

func TestModuleReplyRefusesNonBoundChatID(t *testing.T) {
	rig := newDefaultRig(t)
	rig.bind(t, "111")
	refusals := &recordingRefusals{}
	rig.module.SetRefusalSink(refusals)
	ctx := context.Background()

	for _, chatID := range []int64{222, -100123, -1001234567890, 0} {
		rig.module.reply(ctx, chatID, "private", "hello")
	}
	if calls := rig.doer.methods(); len(calls) != 0 {
		t.Fatalf("a reply reached the transport for a non-owner chat id: %v", calls)
	}
	if len(refusals.events) != 4 || refusals.events[0].Reason != reasonChatNotPaired {
		t.Fatalf("refusals = %+v, want 4 chat-not-paired events", refusals.events)
	}
	client := rig.module.client
	assertChatRefused(t, client.SendMessage(ctx, 222, cascadepa.TierInternal, "x"))
	_, err := client.sendApprovalButtons(ctx, 222, cascadepa.TierInternal, "x", "a", "d")
	assertChatRefused(t, err)
	if calls := rig.doer.methods(); len(calls) != 0 {
		t.Fatalf("a direct send reached the transport: %v", calls)
	}

	rig.module.reply(ctx, 111, "private", "hello")
	if calls := rig.doer.methods(); len(calls) != 1 {
		t.Fatalf("the owner DM got %d calls, want 1", len(calls))
	}
}

func TestOwnerGuardFailsClosed(t *testing.T) {
	ctx := context.Background()
	t.Run("unset guard refuses every send", func(t *testing.T) {
		doer := &fakeDoer{}
		c := NewBotClient(testSubject, doer, &tierGate{}, nil)
		assertChatRefused(t, c.SendMessage(ctx, 111, cascadepa.TierInternal, "x"))
		if len(doer.methods()) != 0 {
			t.Fatal("an unguarded client reached the transport")
		}
	})
	t.Run("store error refuses", func(t *testing.T) {
		doer := &fakeDoer{}
		c := NewBotClient(testSubject, doer, &tierGate{}, nil)
		refusals := &recordingRefusals{}
		if err := c.SetOwnerGuard(failingChats{}, refusals, t0); err != nil {
			t.Fatalf("SetOwnerGuard: %v", err)
		}
		assertChatRefused(t, c.SendMessage(ctx, 111, cascadepa.TierInternal, "x"))
		if len(doer.methods()) != 0 || len(refusals.events) != 1 {
			t.Fatalf("calls %v, refusals %d: a store error must refuse once", doer.methods(), len(refusals.events))
		}
	})
	t.Run("nil arguments are invalid and leave the client closed", func(t *testing.T) {
		c := NewBotClient(testSubject, &fakeDoer{}, &tierGate{}, nil)
		sink := &recordingRefusals{}
		for name, err := range map[string]error{
			"src":   c.SetOwnerGuard(nil, sink, t0),
			"sink":  c.SetOwnerGuard(positiveChats{}, nil, t0),
			"clock": c.SetOwnerGuard(positiveChats{}, sink, nil),
		} {
			if !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Fatalf("nil %s: err = %v, want KindInvalidInput", name, err)
			}
		}
		assertChatRefused(t, c.SendMessage(ctx, 111, cascadepa.TierInternal, "x"))
	})
}

func TestCallbackIDsBoundedAndOneUse(t *testing.T) {
	ctx := context.Background()
	clock := &advancingClock{at: t0()}
	doer := &fakeDoer{}
	c := NewBotClient(testSubject, doer, &tierGate{}, nil)
	if err := c.SetOwnerGuard(positiveChats{}, &recordingRefusals{}, clock.Now); err != nil {
		t.Fatalf("SetOwnerGuard: %v", err)
	}

	c.recordCallback("one")
	if err := c.AnswerCallbackQuery(ctx, "one", cascadepa.TierInternal, "ok"); err != nil {
		t.Fatalf("first answer: %v", err)
	}
	if err := c.AnswerCallbackQuery(ctx, "one", cascadepa.TierInternal, "ok"); err != errCallbackNotAdmitted {
		t.Fatalf("second answer err = %v, want errCallbackNotAdmitted", err)
	}
	if calls := doer.methods(); len(calls) != 1 {
		t.Fatalf("transport calls = %v, want exactly one", calls)
	}
	if n := len(c.guard.Load().callbacks); n != 0 {
		t.Fatalf("an answered id is still answerable (%d ids)", n)
	}
	if c.recordCallback("one") {
		t.Fatal("a replayed admission revived an answered callback id")
	}
	if err := c.AnswerCallbackQuery(ctx, "one", cascadepa.TierInternal, "ok"); err != errCallbackNotAdmitted {
		t.Fatalf("answer after a replayed admission err = %v, want errCallbackNotAdmitted", err)
	}

	for _, id := range []string{"a", "b", "c"} {
		c.recordCallback(id)
	}
	clock.advance(callbackIDTTL - time.Second)
	c.recordCallback("late")
	clock.advance(2 * time.Second)
	if err := c.AnswerCallbackQuery(ctx, "a", cascadepa.TierInternal, "ok"); err != errCallbackNotAdmitted {
		t.Fatalf("expired id err = %v, want errCallbackNotAdmitted", err)
	}
	g := c.guard.Load()
	if len(g.callbacks) != 1 || len(g.answered) != 1 {
		t.Fatalf("stored ids = %d live, %d answered, want the unexpired id and the remembered answer",
			len(g.callbacks), len(g.answered))
	}
	if calls := doer.methods(); len(calls) != 1 {
		t.Fatalf("an expired id reached the transport: %v", calls)
	}
}

func TestBotClient_AnswerCallbackQueryPostsTheGatesOutput(t *testing.T) {
	doer := &fakeDoer{}
	gate := &rewritingGate{}
	c := NewBotClient(testSubject, doer, gate, nil)
	armTestGuard(t, c)
	c.recordCallback("cb-1")
	if err := c.AnswerCallbackQuery(context.Background(), "cb-1", cascadepa.TierInternal, "raw text"); err != nil {
		t.Fatalf("AnswerCallbackQuery: %v", err)
	}
	if got := lastSent(t, doer); got != gateMarker {
		t.Fatalf("answer = %q, want the egress gate's output %q", got, gateMarker)
	}
}

func TestSetOwnerGuardOnlyOnce(t *testing.T) {
	c := NewBotClient(testSubject, &fakeDoer{}, &tierGate{}, nil)
	armTestGuard(t, c)
	err := c.SetOwnerGuard(failingChats{}, &recordingRefusals{}, t0)
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("second SetOwnerGuard err = %v, want KindConflict", err)
	}
	if ok, aerr := c.guard.Load().src.AllowsChat(context.Background(), 5); aerr != nil || !ok {
		t.Fatalf("the first guard was replaced (ok=%v, err=%v)", ok, aerr)
	}
}

func TestUnarmedGuardAdmitsNothing(t *testing.T) {
	ctx := context.Background()
	c := NewBotClient(testSubject, &fakeDoer{}, &tierGate{}, nil)
	if c.allowsOwnerDM(ctx, 111) || c.recordCallback("cb") || c.consumeCallback("cb") {
		t.Fatal("a client with no guard admitted a chat or a callback id")
	}
	c.emitRefusedInbound(ctx, reasonUpdateNotOwnerDM) // no sink, must not panic
	armTestGuard(t, c)
	if c.recordCallback("") {
		t.Fatal("an empty callback id was recorded")
	}
	// A module with no clock cannot arm its client: the client stays closed.
	m := NewTelegramModule(testSubject, NewBotClient(testSubject, &fakeDoer{}, &tierGate{}, nil), nil, nil, nil, nil)
	assertChatRefused(t, m.client.SendMessage(ctx, 111, cascadepa.TierInternal, "x"))
	m = NewTelegramModule(testSubject, nil, nil, nil, nil, fixedTestClock{t0()})
	if m.client != nil {
		t.Fatal("a nil client was replaced")
	}
}
