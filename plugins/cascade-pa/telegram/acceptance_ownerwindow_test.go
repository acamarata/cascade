//go:build integration

// Purpose (this file): P1-CPA-14 acceptance [6] and [11], hermetic over
//   recorded updates. The owner-window order is replayed through the real
//   Poll/dispatch chain: the offset is primed past the backlog, then one
//   getUpdates batch mixes owner DMs with a stranger DM, a stranger /pair,
//   the owner inside a group and a forged owner id in a stranger chat. Only
//   the paired DM is appended, delivered or answered; no other chat is bound
//   or addressed; allowed_updates never changes; no negative (group or
//   channel) chat id is ever addressed.
//
//   [11]: the same replay runs twice, once behind the live harness's own
//   owner filter (harnessOwnerFilter) and once with that filter removed, so
//   the DM-only property is shown to hold in production code (P1-CPA-17).
//
// "Consumed" here means acted on: a foreign update gets no handler, no turn,
//   no delivery, no reply and no binding. Telegram's offset acknowledges
//   every update in a batch whoever sent it; that is the Bot API, not a
//   bridge decision.
//
// SPORT: plugins/cascade-pa/telegram TestOwnerWindowFiltersForeignChats/TEST
//   (P1-CPA-14).

package telegram

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

const (
	windowOwner    = int64(8100)
	windowStranger = int64(8999)
	windowGroup    = int64(-1008100)
	windowBacklog  = int64(5000) // last update id already in the queue before the window opens
)

// harnessOwnerFilter is the live harness's owner filter: it drops every
// getUpdates result that is not a private message from owner in owner's own
// chat. Removing it must change nothing.
type harnessOwnerFilter struct {
	inner Doer
	owner int64
}

func (f harnessOwnerFilter) Do(ctx context.Context, method string, params, out any) error {
	if err := f.inner.Do(ctx, method, params, out); err != nil || method != MethodGetUpdates {
		return err
	}
	got, ok := out.(*[]Update)
	if !ok {
		return nil
	}
	kept := (*got)[:0]
	for _, u := range *got {
		if m := u.Message; m != nil && m.From != nil && m.Chat.Type == "private" &&
			m.Chat.ID == f.owner && m.From.ID == f.owner {
			kept = append(kept, u)
		}
	}
	*got = kept
	return nil
}

func TestOwnerWindowFiltersForeignChats(t *testing.T) {
	for _, filtered := range []bool{true, false} {
		name := "harness filter removed"
		if filtered {
			name = "harness filter on"
		}
		t.Run(name, func(t *testing.T) { runOwnerWindow(t, filtered) })
	}
}

// ownerWindowBatch is the recorded window: owner texts first and last, every
// foreign shape in between, so the last delivery proves all were handled.
func ownerWindowBatch() ([]Update, []string) {
	group := textUpdate(windowBacklog+3, windowOwner, windowGroup, "owner inside a group")
	group.Message.Chat.Type = "group"
	batch := []Update{
		textUpdate(windowBacklog+1, windowOwner, windowOwner, "owner window message one"),
		textUpdate(windowBacklog+2, windowStranger, windowStranger, "stranger direct message"),
		group,
		textUpdate(windowBacklog+4, windowOwner, windowStranger, "forged owner id in a stranger chat"),
		textUpdate(windowBacklog+5, windowStranger, windowStranger, "/pair ABCDEFGH"),
		textUpdate(windowBacklog+6, windowOwner, windowOwner, "owner window message two"),
	}
	return batch, []string{"owner window message one", "owner window message two"}
}

func runOwnerWindow(t *testing.T, filtered bool) {
	r := newAcceptRig(t, "", "")
	threadID := r.pairOwner(t, windowOwner, provider.SensitivityInternal)
	if filtered {
		r.module.client.doer = harnessOwnerFilter{inner: r.doer, owner: windowOwner}
	}
	ctx := context.Background()
	if _, err := r.stores.Updates.Accept(ctx, testSubject, windowBacklog); err != nil {
		t.Fatalf("prime the offset past the backlog: %v", err)
	}
	batch, want := ownerWindowBatch()
	raw, err := json.Marshal(struct {
		OK     bool     `json:"ok"`
		Result []Update `json:"result"`
	}{true, batch})
	if err != nil {
		t.Fatalf("marshal the window batch: %v", err)
	}
	r.doer.push(raw, nil)
	if err := r.bridge.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ch, _ := r.bridge.Receive(ctx)
	got := drainNoLossReceive(t, ch, len(want))
	if err := r.bridge.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	assertNoLossDelivery(t, got, threadID, want)
	assertNoLossAppendTurns(t, r.chat.fakeChatService, threadID, want)
	assertOnlyOwnerAddressed(t, r)
	assertForeignUnbound(t, r)
	assertPollShape(t, r)
}

// assertOnlyOwnerAddressed: every outbound call went to the paired DM, one
// reply per owner message, none to a group or channel id.
func assertOnlyOwnerAddressed(t *testing.T, r *acceptRig) {
	t.Helper()
	sent := r.sendMessages()
	if len(sent) != 2 {
		t.Fatalf("sendMessage calls = %d, want exactly 2 (one reply per owner message)", len(sent))
	}
	for _, p := range sent {
		if p.ChatID != windowOwner {
			t.Fatalf("a sendMessage addressed chat %d, want only the paired DM", p.ChatID)
		}
	}
	for _, m := range r.doer.methods() {
		if m != MethodGetUpdates && m != MethodSendMessage {
			t.Fatalf("unexpected Bot API method %q in the window", m)
		}
	}
}

// assertForeignUnbound: no foreign chat or sender gained a binding or a
// thread, and the owner's own binding is unchanged.
func assertForeignUnbound(t *testing.T, r *acceptRig) {
	t.Helper()
	ctx := context.Background()
	for _, id := range []int64{windowStranger, windowGroup} {
		if ok, err := r.stores.Binding.IsAllowed(ctx, testSubject, strconv.FormatInt(id, 10)); err != nil || ok {
			t.Fatalf("a foreign id is allowed after the window (ok=%v err=%v)", ok, err)
		}
		if _, ok, err := r.threads.ThreadFor(ctx, id); err != nil || ok {
			t.Fatalf("a foreign chat has a thread after the window (ok=%v err=%v)", ok, err)
		}
	}
	if ok, err := r.stores.Binding.IsAllowed(ctx, testSubject, strconv.FormatInt(windowOwner, 10)); err != nil || !ok {
		t.Fatalf("the owner binding was lost (ok=%v err=%v)", ok, err)
	}
}

// assertPollShape: the first getUpdates starts past the backlog and no call
// ever changes allowed_updates.
func assertPollShape(t *testing.T, r *acceptRig) {
	t.Helper()
	r.doer.mu.Lock()
	defer r.doer.mu.Unlock()
	var polls []getUpdatesParams
	for _, c := range r.doer.calls {
		if p, ok := c.params.(getUpdatesParams); ok && c.method == MethodGetUpdates {
			polls = append(polls, p)
		}
	}
	if len(polls) == 0 {
		t.Fatal("no getUpdates call was recorded")
	}
	if polls[0].Offset != windowBacklog+1 {
		t.Fatalf("first getUpdates offset = %d, want %d (primed past the backlog)", polls[0].Offset, windowBacklog+1)
	}
	for _, p := range polls {
		if !reflect.DeepEqual(p.AllowedUpdates, polls[0].AllowedUpdates) ||
			!reflect.DeepEqual(p.AllowedUpdates, []string{"message", "callback_query"}) {
			t.Fatalf("allowed_updates changed across polls: %v", p.AllowedUpdates)
		}
	}
}
