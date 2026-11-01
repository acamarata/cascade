package conversation

// Purpose: the assistant turn and its conversation_reply row commit once,
//   together, through the journaled append; a repeat chat.reply replays.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestP1ChatReplyPersistence(t *testing.T) {
	f := newReplyFixture(t, newScriptDoor("Hello", ", ", "world"), 4000)
	seedOwnedThread(t, f.store, "th", provider.SensitivityRestricted)
	user := seedReplyTurn(t, f.store, "th", RoleUser, OriginLocal, "", "greet me")
	res, err := f.openAndReply(t, ownerCLICtx(), "th", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Replayed || res.ThreadID != "th" || res.TurnID != user.ID || res.AssistantTurnID == "" {
		t.Fatalf("result = %+v", res)
	}
	ctx := context.Background()
	turns := storedTurns(t, f.store, "th")
	segs, err := f.store.ListSegments(ctx, res.AssistantTurnID)
	if err != nil || len(turns) != 2 || turns[1].ID != res.AssistantTurnID || turns[1].Role != RoleAssistant ||
		len(segs) != 1 || segs[0].Content != "Hello, world" {
		t.Fatalf("stored turns %+v segments %+v (%v), want the whole reply as one assistant turn", turns, segs, err)
	}
	cs := f.store.(*conversationStore)
	if mapped, ok, err := cs.replyFor(ctx, user.ID); err != nil || !ok || mapped != res.AssistantTurnID {
		t.Fatalf("reply mapping = %q %v %v, want %s", mapped, ok, err, res.AssistantTurnID)
	}
	acks, err := f.js.Replay(ctx, "th", journal.Cursor{}, []journal.Kind{journal.KindAck})
	if err != nil || len(acks) != 1 {
		t.Fatalf("journal acks = %d (%v), want 1", len(acks), err)
	}
	var p journalPayload
	if err := json.Unmarshal(acks[0].Payload, &p); err != nil || p.ReplyTo != user.ID || p.Turn == nil || p.Turn.ID != res.AssistantTurnID {
		t.Fatalf("journal payload = %+v (%v), want the turn and its mapping", p, err)
	}
	// One transaction: a second reply row for the same user turn rolls its turn back.
	next := Turn{ID: NewTurnID("th", 2, RoleAssistant), ThreadID: "th", Seq: 2, Role: RoleAssistant, CreatedAt: 5}
	wantSentinel(t, cs.appendReply(ctx, user.ID, next, nil), ErrImmutable)
	if n := len(storedTurns(t, f.store, "th")); n != 2 {
		t.Fatalf("turns after a refused mapping = %d, want 2 (the turn rolled back with it)", n)
	}
}

func TestReplyIdempotentByUserTurn(t *testing.T) {
	f := newReplyFixture(t, newScriptDoor("once"), 4000)
	seedOwnedThread(t, f.store, "th", provider.SensitivityRestricted)
	user := seedReplyTurn(t, f.store, "th", RoleUser, OriginLocal, "", "hi")
	call := func() ReplyResult {
		t.Helper()
		opened, eo := f.reg.Dispatch(ownerCLICtx(), &rpc.Request{JSONRPC: "2.0", Method: MethodReplyOpen,
			Params: json.RawMessage(`{"thread_id":"th","turn_id":"` + user.ID + `"}`)})
		if eo != nil {
			t.Fatalf("chat.reply_open: %+v", eo)
		}
		id := opened.(replyOpenResult).StreamID
		res, eo := f.reg.Dispatch(ownerCLICtx(), &rpc.Request{JSONRPC: "2.0", Method: MethodReply,
			Params: json.RawMessage(`{"stream_id":"` + id + `"}`)})
		if eo != nil {
			t.Fatalf("chat.reply: %+v", eo)
		}
		return res.(ReplyResult)
	}
	first, second := call(), call()
	if first.Replayed || !second.Replayed || second.AssistantTurnID != first.AssistantTurnID {
		t.Fatalf("first %+v second %+v, want the second replayed with the same assistant turn", first, second)
	}
	if n := f.door.streamCalls(); n != 1 {
		t.Fatalf("provider dispatches = %d, want exactly 1", n)
	}
	if n := len(storedTurns(t, f.store, "th")); n != 2 {
		t.Fatalf("turns = %d, want 2", n)
	}
}

func TestReplyInflightRefused(t *testing.T) {
	door := newScriptDoor("slow")
	door.gate = make(chan struct{})
	f := newReplyFixture(t, door, 4000)
	seedOwnedThread(t, f.store, "th", provider.SensitivityRestricted)
	user := seedReplyTurn(t, f.store, "th", RoleUser, OriginLocal, "", "hi")
	ctx := ownerCLICtx()
	a, err := f.replier.open(ctx, "th", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.replier.open(ctx, "th", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.replier.Reply(ctx, ReplyRequest{StreamID: a})
		done <- err
	}()
	<-door.started // the first reply is dispatching
	_, err = f.replier.Reply(ctx, ReplyRequest{StreamID: b})
	wantSentinel(t, err, errReplyInProgress)
	close(door.gate)
	if err := <-done; err != nil {
		t.Fatalf("first reply: %v", err)
	}
	if n := door.streamCalls(); n != 1 {
		t.Fatalf("provider dispatches = %d, want exactly 1", n)
	}
}
