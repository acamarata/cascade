// Purpose (this file): the chat-to-thread map as TelegramBridge uses it:
//   IsBridgeThread is a map lookup only, Send resolves its chat through the
//   map and the binding, Bind admits only the owner's direct chat, and the
//   inbound path mints, adopts or refuses exactly as chat_threads.go says.
//
// SPORT: plugins/cascade-pa/telegram ThreadMap/TEST.

package telegram

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"testing"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestIsBridgeThread_MapLookupOnly: a chat's first message mints a thread
// and binds it; that minted id is a bridge thread, while the chat's
// prefix-derived legacy id (never bound) and an unknown id are not. A map
// read error is an error, never a yes. Failing input: deciding by prefix
// (the legacy id reads true and the minted one false).
func TestIsBridgeThread_MapLookupOnly(t *testing.T) {
	r := newBridgeRig(t)
	r.legacy.exists = false
	r.bind(t, "7001")
	r.privacy.set("thread-fake-1", cascadepa.TierInternal)
	r.dispatchText(t, "7001", 7001, "first")
	minted, ok, err := r.threads.ThreadFor(context.Background(), 7001)
	if err != nil || !ok || minted != "thread-fake-1" {
		t.Fatalf("ThreadFor(7001) = %q, %v, %v; want the minted thread-fake-1", minted, ok, err)
	}
	for threadID, want := range map[string]bool{minted: true, threadIDForChat(7001): false, "thread-unknown": false} {
		if got, err := r.bridge.IsBridgeThread(context.Background(), threadID); err != nil || got != want {
			t.Errorf("IsBridgeThread(%q) = %v, %v; want %v", threadID, got, err, want)
		}
	}
	r.mapState.mu.Lock()
	r.mapState.loadErr = errors.New("test: map unreadable")
	r.mapState.mu.Unlock()
	if got, err := r.bridge.IsBridgeThread(context.Background(), minted); got || err == nil {
		t.Fatalf("IsBridgeThread with an unreadable map = %v, %v; want false and the error", got, err)
	}
}

// TestChatBridge_Send_UnmappedThread_SendsNothing: a thread the map does
// not hold is not a bridge thread even when its id names the bound chat
// and its tier would allow; Send refuses KindNotFound and sends 0 bytes.
// The same chat's MAPPED thread then delivers, so the refusal is the map.
func TestChatBridge_Send_UnmappedThread_SendsNothing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Send refuses at the platform gate first on Windows (chat_windows_test.go)")
	}
	r := newBridgeRig(t)
	r.bind(t, "7002")
	for _, threadID := range []string{threadIDForChat(7002), "thread-not-mapped"} {
		r.privacy.set(threadID, cascadepa.TierPublic)
		err := r.bridge.Send(context.Background(), threadID, []byte("unmapped body"))
		wantMsg := fmt.Sprintf("not-found: cascade-pa/telegram: thread %q does not map to a chat this bridge serves", threadID)
		if !cascade.HasKind(err, cascade.KindNotFound) || err == nil || err.Error() != wantMsg {
			t.Fatalf("Send(%q) = %v, want KindNotFound %q", threadID, err, wantMsg)
		}
	}
	if got := r.doer.methods(); len(got) != 0 {
		t.Fatalf("an unmapped Send reached the transport: %v", got)
	}
	mapped := r.pairLegacy(t, 7002)
	r.privacy.set(mapped, cascadepa.TierPublic)
	if err := r.bridge.Send(context.Background(), mapped, []byte("mapped body")); err != nil {
		t.Fatalf("Send on the mapped thread: %v", err)
	}
	if sent := lastSent(t, r.doer); sent != "mapped body" {
		t.Fatalf("sent %q, want the mapped body", sent)
	}
}

// TestChatBridge_Send_ForeignChatThread_Refuses: a thread the map binds to a
// chat other than the paired sender (a row the owner-DM rule would never
// write, seeded straight into the store) refuses errSendNotBound with zero
// transport calls, even at an allowing tier.
func TestChatBridge_Send_ForeignChatThread_Refuses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Send refuses at the platform gate first on Windows (chat_windows_test.go)")
	}
	r := newBridgeRig(t)
	r.bind(t, "7003")
	if _, err := r.mapStore.Bind(context.Background(), testSubject, 7777, "thread-foreign"); err != nil {
		t.Fatalf("seed foreign row: %v", err)
	}
	r.privacy.set("thread-foreign", cascadepa.TierPublic)
	err := r.bridge.Send(context.Background(), "thread-foreign", []byte("foreign body"))
	if !errors.Is(err, errSendNotBound) || err.Error() != errSendNotBound.Error() {
		t.Fatalf("Send to a foreign chat's thread = %v, want %v", err, errSendNotBound)
	}
	if got := r.doer.methods(); len(got) != 0 {
		t.Fatalf("a foreign-chat Send reached the transport: %v", got)
	}
}

// TestThreadMapBindOwnerDMOnly: Bind refuses a group, a supergroup, a
// channel, a chat.id != from.id, an unpaired sender and another chat's
// legacy id, each errBindNotOwnerDM with nothing written; the owner's own
// DM then binds, so the refusals are the rule and not a broken store.
func TestThreadMapBindOwnerDMOnly(t *testing.T) {
	r := newBridgeRig(t)
	r.bind(t, "7004")
	ctx := context.Background()
	refused := []struct {
		chat   Chat
		from   int64
		thread string
	}{
		{Chat{ID: -7004, Type: "group"}, 7004, "thread-a"},
		{Chat{ID: -1007004, Type: "supergroup"}, 7004, "thread-a"},
		{Chat{ID: -1007005, Type: "channel"}, 7004, "thread-a"},
		{Chat{ID: 7005, Type: "private"}, 7004, "thread-a"},
		{Chat{ID: 7006, Type: "private"}, 7006, "thread-a"},
		{Chat{ID: 7004, Type: "private"}, 7004, threadIDForChat(7005)},
	}
	for _, c := range refused {
		if _, err := r.threads.Bind(ctx, c.chat, c.from, c.thread); !errors.Is(err, errBindNotOwnerDM) ||
			err.Error() != errBindNotOwnerDM.Error() {
			t.Errorf("Bind(%+v, from %d, %s) = %v, want %v", c.chat, c.from, c.thread, err, errBindNotOwnerDM)
		}
	}
	if n := r.mapState.saveCall; n != 0 {
		t.Fatalf("refused binds wrote %d map rows, want 0", n)
	}
	bound, err := r.threads.Bind(ctx, Chat{ID: 7004, Type: "private"}, 7004, "thread-a")
	if err != nil || bound != "thread-a" {
		t.Fatalf("owner DM Bind = %q, %v; want thread-a", bound, err)
	}
}

// TestBridgeInboundFailsClosedOnMapAndMint: a map read error, a legacy
// lookup error and a create_thread error each refuse the inbound message:
// no append, no reply, a RefusalReport entry, and no fall back to the
// chat's derived id.
func TestBridgeInboundFailsClosedOnMapAndMint(t *testing.T) {
	for name, breakIt := range map[string]func(r *bridgeRig){
		"map read":    func(r *bridgeRig) { r.mapState.loadErr = errors.New("test: map down") },
		"legacy read": func(r *bridgeRig) { r.legacy.err = errors.New("test: lookup down") },
		"create":      func(r *bridgeRig) { r.legacy.exists, r.chat.createErr = false, errors.New("test: mint down") },
	} {
		r := newBridgeRig(t)
		r.bind(t, "7007")
		r.privacy.set(threadIDForChat(7007), cascadepa.TierInternal)
		breakIt(r)
		r.dispatchText(t, "7007", 7007, "never recorded")
		if n := r.chat.callCount(); n != 0 {
			t.Fatalf("%s: AppendTurn called %d times, want 0", name, n)
		}
		if sent := r.doer.sentTexts(); len(sent) != 0 {
			t.Fatalf("%s: replies %q, want none", name, sent)
		}
		if rep := r.bridge.RefusalReport(); len(rep) != 1 || rep[0].Reason != reasonNoBridgeThread {
			t.Fatalf("%s: RefusalReport = %+v, want one %q", name, rep, reasonNoBridgeThread)
		}
	}
}

// TestMayBeBridgeThread pins the necessary-condition pre-filter: only the
// empty id and non-canonical legacy forms are "never".
func TestMayBeBridgeThread(t *testing.T) {
	for threadID, want := range map[string]bool{"": false, threadIDForChat(1): true, bridgeThreadPrefix + "+1": false,
		bridgeThreadPrefix + "01": false, bridgeThreadPrefix: false, "thread-abc": true, "anything": true} {
		if got := MayBeBridgeThread(threadID); got != want {
			t.Errorf("MayBeBridgeThread(%q) = %v, want %v", threadID, got, want)
		}
	}
}

// TestUnwiredThreadSeamsRefuse: a bridge built without a map or a legacy
// lookup refuses every call rather than inventing a thread.
func TestUnwiredThreadSeamsRefuse(t *testing.T) {
	m := threadMapOrRefuseAll(nil)
	if _, _, err := m.ThreadFor(context.Background(), 1); err != errNoThreadMap {
		t.Fatalf("ThreadFor = %v, want errNoThreadMap", err)
	}
	if _, _, err := m.ChatFor(context.Background(), "t"); err != errNoThreadMap {
		t.Fatalf("ChatFor = %v, want errNoThreadMap", err)
	}
	if _, err := m.Bind(context.Background(), Chat{ID: 1, Type: "private"}, 1, "t"); err != errNoThreadMap {
		t.Fatalf("Bind = %v, want errNoThreadMap", err)
	}
	if _, err := legacyOrRefuseAll(nil).ThreadExists(context.Background(), "t"); err != errNoLegacyLookup {
		t.Fatalf("ThreadExists = %v, want errNoLegacyLookup", err)
	}
	if _, err := (unconfiguredChatService{}).CreateThread(context.Background(), "internal"); err != errNoChatService {
		t.Fatalf("CreateThread = %v, want errNoChatService", err)
	}
	if _, ok := NewThreadMap(nil, nil, testSubject).(unconfiguredThreadMap); !ok {
		t.Fatal("NewThreadMap(nil store) is not the refusing default")
	}
}
