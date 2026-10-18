package telegram

// Purpose (this file): pairing is owner-initiated and silent on every
//   failure. The only pairing reply is the success confirmation, sent to the
//   private chat that was just bound.
//
// SPORT: plugins/cascade-pa/telegram pairing-silent/TEST (P1-CPA-17).

import (
	"context"
	"testing"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

func TestPairFromGroupChatRefusedSilently(t *testing.T) {
	rig := newDefaultRig(t)
	refusals := &recordingRefusals{}
	rig.module.SetRefusalSink(refusals)
	code := issue(t, rig)
	rig.module.dispatch(context.Background(), groupUpdate(1, 111, -100123, "supergroup", "/pair "+code))
	assertSilent(t, rig, refusals)
	if rig.allows(t, "111") {
		t.Fatal("a /pair from a group bound the sender")
	}
	pending, err := rig.stores.Pairing.Pending(context.Background(), testSubject)
	if err != nil || !pending {
		t.Fatalf("a group /pair consumed the outstanding code (pending=%v, err=%v)", pending, err)
	}
}

func TestPairWrongCodeNoReply(t *testing.T) {
	rig := newDefaultRig(t)
	refusals := &recordingRefusals{}
	rig.module.SetRefusalSink(refusals)
	_ = issue(t, rig)
	rig.module.dispatch(context.Background(), textUpdate(1, 111, 111, "/pair 00000000"))
	assertSilent(t, rig, refusals)
	if rig.allows(t, "111") {
		t.Fatal("a wrong code bound the sender")
	}
}

func TestPairFromNonPositiveIDsRefusedSilently(t *testing.T) {
	for _, id := range []int64{0, -5} {
		rig := newDefaultRig(t)
		refusals := &recordingRefusals{}
		rig.module.SetRefusalSink(refusals)
		code := issue(t, rig)
		rig.module.dispatch(context.Background(), textUpdate(1, id, id, "/pair "+code))
		assertSilent(t, rig, refusals)
		pending, err := rig.stores.Pairing.Pending(context.Background(), testSubject)
		if err != nil || !pending {
			t.Fatalf("id %d consumed the outstanding code (pending=%v, err=%v)", id, pending, err)
		}
	}
}

func TestPairLockoutNoReply(t *testing.T) {
	rig := newDefaultRig(t)
	refusals := &recordingRefusals{}
	rig.module.SetRefusalSink(refusals)
	_ = issue(t, rig)
	for i := int64(1); i <= cascadepa.PairCodeMaxAttempts; i++ {
		rig.module.dispatch(context.Background(), textUpdate(i, 111, 111, "/pair ZZZZZZZZ"))
	}
	if calls := rig.doer.methods(); len(calls) != 0 {
		t.Fatalf("the lockout path made Bot API calls: %v", calls)
	}
	if rig.sink.count() != 1 {
		t.Fatalf("lockout events = %d, want 1", rig.sink.count())
	}
	if len(refusals.events) != cascadepa.PairCodeMaxAttempts {
		t.Fatalf("bridge.refused events = %d, want one per attempt (%d)",
			len(refusals.events), cascadepa.PairCodeMaxAttempts)
	}
}

func TestPairSuccessRepliesOnlyToTheBoundDM(t *testing.T) {
	rig := newDefaultRig(t)
	code := issue(t, rig)
	rig.module.dispatch(context.Background(), textUpdate(1, 111, 111, "/pair "+code))
	if calls := rig.doer.methods(); len(calls) != 1 || calls[0] != MethodSendMessage {
		t.Fatalf("transport calls = %v, want only the confirmation", calls)
	}
	if got := rig.doer.calls[0].params; got != (sendMessageParams{ChatID: 111, Text: replyPaired + testSubject}) {
		t.Fatalf("confirmation params = %+v", got)
	}
}
