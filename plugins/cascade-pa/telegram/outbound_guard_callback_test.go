package telegram

// Purpose (this file): the callback-id half of the owner guard: one-use ids,
//   the replay and retention windows, the cap, and the owner-DM callback flow.
//
// SPORT: plugins/cascade-pa/telegram owner-guard-callbacks/TEST (P1-CPA-17).

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestCallbackReplayWindows(t *testing.T) {
	clock := &advancingClock{at: t0()}
	c := NewBotClient(testSubject, &fakeDoer{}, &tierGate{}, nil)
	if err := c.SetOwnerGuard(positiveChats{}, &recordingRefusals{}, clock.Now); err != nil {
		t.Fatalf("SetOwnerGuard: %v", err)
	}
	if !c.recordCallback("x") {
		t.Fatal("first admission refused")
	}
	deadline := c.guard.Load().callbacks["x"]
	clock.advance(30 * time.Second)
	if c.recordCallback("x") {
		t.Fatal("a duplicate admission of a live id was accepted")
	}
	if got := c.guard.Load().callbacks["x"]; !got.Equal(deadline) {
		t.Fatalf("a duplicate admission slid the deadline from %v to %v", deadline, got)
	}
	if !c.consumeCallback("x") {
		t.Fatal("the live id was not answerable")
	}
	clock.advance(callbackIDTTL + time.Second)
	if c.recordCallback("x") {
		t.Fatal("an answered id was re-admitted after the answer window but inside the retention window")
	}
	clock.advance(answeredRetention)
	if !c.recordCallback("x") {
		t.Fatal("the retention horizon never ends: the tombstone set is not bounded in time")
	}
}

func TestCallbackSetIsCapped(t *testing.T) {
	c := NewBotClient(testSubject, &fakeDoer{}, &tierGate{}, nil)
	armTestGuard(t, c)
	admitted := 0
	for i := 0; i < maxCallbackIDs+10; i++ {
		if c.recordCallback(fmt.Sprintf("cb-%d", i)) {
			admitted++
		}
	}
	if admitted != maxCallbackIDs {
		t.Fatalf("admitted %d ids, want the cap %d", admitted, maxCallbackIDs)
	}
}

func callbackUnrecordedIDNotAnswered(t *testing.T) {
	rig, _ := foreignRig(t)
	rig.module.Answer(context.Background(), "never-admitted", "private", "ok")
	if calls := rig.doer.methods(); len(calls) != 0 {
		t.Fatalf("an unrecorded callback id was answered: %v", calls)
	}
}

func callbackReplayAdmittedOnce(t *testing.T) {
	rig, refusals := foreignRig(t)
	answers := 0
	rig.module.RegisterHandler(HandlerCallbackQuery, func(ctx context.Context, msg InboundMessage) error {
		rig.module.Answer(ctx, msg.Update.CallbackQuery.ID, "private", "ok")
		answers++
		return nil
	})
	rig.module.dispatch(context.Background(), callbackUpdate(1, 111, "approve:req-1"))
	rig.module.dispatch(context.Background(), callbackUpdate(2, 111, "approve:req-1"))
	if calls := rig.doer.methods(); len(calls) != 1 || answers != 1 {
		t.Fatalf("transport calls %v, handler runs %d: a replayed callback id must be admitted once", calls, answers)
	}
	if len(refusals.events) != 1 || refusals.events[0].Reason != reasonCallbackRefused {
		t.Fatalf("refusals = %+v, want one %q for the replay", refusals.events, reasonCallbackRefused)
	}
}

func callbackOwnerAnsweredOnce(t *testing.T) {
	rig, refusals := foreignRig(t)
	rig.module.RegisterHandler(HandlerCallbackQuery, func(ctx context.Context, msg InboundMessage) error {
		rig.module.Answer(ctx, msg.Update.CallbackQuery.ID, "private", "ok")
		rig.module.Answer(ctx, msg.Update.CallbackQuery.ID, "private", "again")
		return nil
	})
	rig.module.dispatch(context.Background(), callbackUpdate(1, 111, "approve:req-1"))
	if calls := rig.doer.methods(); len(calls) != 1 || calls[0] != MethodAnswerCallbackQuery {
		t.Fatalf("transport calls = %v, want exactly one answerCallbackQuery", calls)
	}
	if len(refusals.events) != 0 {
		t.Fatalf("an owner callback produced %d refusals", len(refusals.events))
	}
}
