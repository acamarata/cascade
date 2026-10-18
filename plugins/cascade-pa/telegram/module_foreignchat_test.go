package telegram

// Purpose (this file): the inbound half of the owner-DM-only rule. Every
//   update that is not the paired owner's private chat is consumed silently:
//   zero Bot API calls of any kind on the recording transport, one
//   content-free bridge.refused, and no handler run.
//
// SPORT: plugins/cascade-pa/telegram foreign-chat/TEST (P1-CPA-17).

import (
	"context"
	"testing"
)

// foreignRig is a rig bound to owner "111" with a recording refusal sink and a
// handler that fails the test when it runs.
func foreignRig(t *testing.T) (*testRig, *recordingRefusals) {
	t.Helper()
	rig := newDefaultRig(t)
	rig.bind(t, "111")
	refusals := &recordingRefusals{}
	rig.module.SetRefusalSink(refusals)
	for _, kind := range []string{HandlerText, HandlerCallbackQuery} {
		rig.module.RegisterHandler(kind, func(context.Context, InboundMessage) error {
			t.Error("a handler ran for an update outside the owner DM")
			return nil
		})
	}
	return rig, refusals
}

// assertSilent fails unless the update produced zero transport calls and
// exactly one content-free bridge.refused.
func assertSilent(t *testing.T, rig *testRig, refusals *recordingRefusals) {
	t.Helper()
	if calls := rig.doer.methods(); len(calls) != 0 {
		t.Fatalf("the bridge made Bot API calls for a non-owner update: %v", calls)
	}
	if len(refusals.events) != 1 {
		t.Fatalf("bridge.refused events = %d, want exactly 1", len(refusals.events))
	}
	e := refusals.events[0]
	if e.Reason != reasonUpdateNotOwnerDM || e.ThreadID != "" || e.ResolvedTier != "" ||
		e.CorrelationID != "" || !e.At.Equal(t0()) {
		t.Fatalf("event = %+v, want only reason %q at the injected clock", e, reasonUpdateNotOwnerDM)
	}
}

func groupUpdate(id, fromID, chatID int64, kind, text string) Update {
	u := textUpdate(id, fromID, chatID, text)
	u.Message.Chat.Type = kind
	return u
}

func TestModuleForeignChatGetsNoReply(t *testing.T) {
	cases := map[string]Update{
		"stranger DM":      textUpdate(1, 999, 999, "hello"),
		"stranger /pair":   textUpdate(2, 999, 999, "/pair ABCDEFGH"),
		"owner id, forged": textUpdate(3, 111, 999, "hello"),
	}
	for name, u := range cases {
		t.Run(name, func(t *testing.T) {
			rig, refusals := foreignRig(t)
			rig.module.dispatch(context.Background(), u)
			assertSilent(t, rig, refusals)
		})
	}
}

func TestModuleOwnerInGroupChatGetsNoReply(t *testing.T) {
	for _, kind := range []string{"group", "supergroup"} {
		t.Run(kind, func(t *testing.T) {
			rig, refusals := foreignRig(t)
			rig.module.dispatch(context.Background(), groupUpdate(1, 111, -100123, kind, "hello"))
			assertSilent(t, rig, refusals)
		})
	}
	t.Run("owner id as a group chat id", func(t *testing.T) {
		rig, refusals := foreignRig(t)
		rig.module.dispatch(context.Background(), groupUpdate(1, 111, 111, "group", "hello"))
		assertSilent(t, rig, refusals)
	})
}

func TestModuleForeignChannelPostIgnored(t *testing.T) {
	rig, refusals := foreignRig(t)
	post := groupUpdate(1, 0, -1001234567890, "channel", "channel post")
	post.Message.From = nil
	rig.module.dispatch(context.Background(), post)
	assertSilent(t, rig, refusals)
}

func TestModuleUnknownUpdateShapeIgnored(t *testing.T) {
	rig, refusals := foreignRig(t)
	rig.module.dispatch(context.Background(), Update{UpdateID: 9})
	assertSilent(t, rig, refusals)
}

func TestModuleBindingStoreErrorNoReply(t *testing.T) {
	rig, refusals := foreignRig(t)
	rig.state.loadErr = errTestStoreDown
	rig.module.dispatch(context.Background(), textUpdate(1, 111, 111, "hello"))
	assertSilent(t, rig, refusals)
}

func TestModuleForeignSecretShapedTextNoReply(t *testing.T) {
	cases := map[string]Update{
		"foreign private chat": textUpdate(1, 999, 999, witnessSecretText),
		"owner in a group":     groupUpdate(2, 111, -100123, "supergroup", witnessSecretText),
		"channel post":         groupUpdate(3, 0, -100456, "channel", witnessSecretText),
	}
	cases["channel post"].Message.From = nil
	for name, u := range cases {
		t.Run(name, func(t *testing.T) {
			rig, refusals := foreignRig(t)
			rig.module.secretScanner = secretScannerFor(witnessSecretText, "api-key")
			quarantine := &recordingQuarantineSink{}
			rig.module.quarantine = quarantine
			rig.module.dispatch(context.Background(), u)
			assertSilent(t, rig, refusals)
			if len(quarantine.events) != 0 {
				t.Fatalf("a non-owner chat's text was scanned and recorded: %+v", quarantine.events)
			}
		})
	}
}

func TestCallbackOutsideOwnerDMNotAnswered(t *testing.T) {
	foreign := func(mutate func(*CallbackQuery)) Update {
		u := callbackUpdate(1, 111, "approve:req-1")
		mutate(u.CallbackQuery)
		return u
	}
	cases := map[string]Update{
		"foreign user DM": callbackUpdate(1, 999, "approve:req-1"),
		"owner in group": foreign(func(cq *CallbackQuery) {
			cq.Message.Chat = Chat{ID: -100123, Type: "supergroup"}
		}),
		"non-owner user in the owner chat": foreign(func(cq *CallbackQuery) { cq.From.ID = 999 }),
		"private type, mismatched chat id": foreign(func(cq *CallbackQuery) { cq.Message.Chat.ID = 222 }),
		"group type, matching chat id":     foreign(func(cq *CallbackQuery) { cq.Message.Chat.Type = "group" }),
		"inline callback, no message":      foreign(func(cq *CallbackQuery) { cq.Message = nil }),
	}
	for name, u := range cases {
		t.Run(name, func(t *testing.T) {
			rig, refusals := foreignRig(t)
			rig.module.dispatch(context.Background(), u)
			assertSilent(t, rig, refusals)
		})
	}
	t.Run("unrecorded callback id is not answered", callbackUnrecordedIDNotAnswered)
	t.Run("replayed callback id is admitted once", callbackReplayAdmittedOnce)
	t.Run("owner DM is answered once", callbackOwnerAnsweredOnce)
}
