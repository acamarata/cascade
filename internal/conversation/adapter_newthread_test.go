package conversation

// Purpose (this file): the rule that a caller who named no thread is
//   starting one, and that the id comes from the server.
// SPORT: internal.conversation.adapter (CHANGED — thread minting,
//   P1-E20-W5-S43-T4).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestAppendTurnMintsAThreadWhenNobodyNamedOne is the behaviour both
// `cascade chat` (no --thread) and `cascade_cpa_send` (no thread_id)
// depend on. It used to be a malformed-payload refusal, which made a
// first conversation impossible from either surface.
func TestAppendTurnMintsAThreadWhenNobodyNamedOne(t *testing.T) {
	a, _, _ := newTestAdapter(t)
	raw := json.RawMessage(`{"thread_id":"","role":"user","segments":[{"kind":"text","content":"hi"}]}`)

	out, err := a.handleAppendTurn(context.Background(), raw)
	if err != nil {
		t.Fatalf("append with no thread: %v", err)
	}
	res, ok := out.(appendTurnResult)
	if !ok {
		t.Fatalf("result type %T", out)
	}
	if res.ThreadID == "" {
		t.Fatal("the result names no thread, so the caller cannot continue the conversation it just started")
	}
	if !strings.HasPrefix(res.ThreadID, "thread-") {
		t.Errorf("minted id %q does not carry the thread- prefix", res.ThreadID)
	}
	if res.TurnID == "" {
		t.Error("the result names no turn")
	}

	// The turn really is in the thread the result named. A minted id that
	// nothing was written under would satisfy every assertion above.
	turns, err := a.store.ListTurns(context.Background(), res.ThreadID)
	if err != nil {
		t.Fatalf("ListTurns: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("the minted thread holds %d turn(s), want the one just appended", len(turns))
	}
}

// TestTwoUnnamedAppendsStartTwoThreads is why the id is not content
// addressed: two operators typing the same first message are starting two
// conversations, and hashing the message would merge them.
func TestTwoUnnamedAppendsStartTwoThreads(t *testing.T) {
	a, _, _ := newTestAdapter(t)
	raw := json.RawMessage(`{"role":"user","segments":[{"kind":"text","content":"hi"}]}`)

	first, err := a.handleAppendTurn(context.Background(), raw)
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	second, err := a.handleAppendTurn(context.Background(), raw)
	if err != nil {
		t.Fatalf("second append: %v", err)
	}
	a1, _ := first.(appendTurnResult)
	a2, _ := second.(appendTurnResult)
	if a1.ThreadID == a2.ThreadID {
		t.Fatalf("two unnamed appends landed in one thread (%s); identical opening messages would merge "+
			"two people's conversations", a1.ThreadID)
	}
}

// TestAppendTurnStillRefusesAMissingRole keeps the relaxation narrow: an
// empty thread is a request to start one, an empty role is nobody
// speaking.
func TestAppendTurnStillRefusesAMissingRole(t *testing.T) {
	a, _, _ := newTestAdapter(t)
	raw := json.RawMessage(`{"segments":[{"kind":"text","content":"hi"}]}`)

	if _, err := a.handleAppendTurn(context.Background(), raw); err == nil {
		t.Fatal("a turn with no role was accepted")
	}
}
