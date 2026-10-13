package plugins

// Purpose (this file): the Telegram bridge gets every thread from
//   chat.create_thread through the durable chat-to-thread map, proven on the
//   real composition (mapRig, cascadepa_bridge_chat_wiring_threads_test.go):
//   first message mints and binds, the binding survives a restart, a lost
//   race ends on the winner's thread, legacy threads are adopted (and stay
//   restricted when unmarked), groups never bind, and any map error refuses
//   inbound and outbound with nothing appended and nothing sent.
//
// SPORT: internal/plugins:cascadepa-bridge-chat-wiring (TEST: thread map).

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/bridge"
	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
	"github.com/acamarata/cascade/plugins/cascade-pa/tools"
)

var legacyOwnerThread = "bridge-telegram:" + jsonInt(mapOwner)

// seedLegacyThread writes the owner's legacy thread with one turn, marked
// tier first when marked is true.
func (r *mapRig) seedLegacyThread(t *testing.T, marked bool) {
	t.Helper()
	ctx := context.Background()
	if marked {
		if err := r.store.SetThreadPrivacy(ctx, legacyOwnerThread, provider.SensitivityInternal); err != nil {
			t.Fatalf("mark legacy thread: %v", err)
		}
	}
	turn := conversation.Turn{ID: conversation.NewTurnID(legacyOwnerThread, 0, conversation.RoleUser),
		ThreadID: legacyOwnerThread, Role: conversation.RoleUser, CreatedAt: 1}
	if err := r.store.AppendTurn(ctx, turn); err != nil {
		t.Fatalf("seed legacy turn: %v", err)
	}
}

func TestBridgeFirstMessageMintsThroughCreateThread(t *testing.T) {
	r := newMapRig(t)
	r.deliver(t, textUpdateJSON(1, mapOwner, "private", "hello"))
	minted := r.daemon.mintedIDs()
	if len(minted) != 1 || !strings.HasPrefix(minted[0], "thread-") {
		t.Fatalf("create_thread minted %v, want exactly one daemon-minted id", minted)
	}
	if modes := r.daemon.calls; modes[0].method != conversation.MethodCreateThread || modes[0].params["privacy_mode"] != "internal" {
		t.Fatalf("first daemon call = %+v, want chat.create_thread {privacy_mode: internal}", modes[0])
	}
	if appends := r.daemon.threadsOf(conversation.MethodAppendTurn); len(appends) != 1 || appends[0] != minted[0] {
		t.Fatalf("append_turn threads = %v, want one append to %s", appends, minted[0])
	}
	if bound, ok := r.mapped(t, mapOwner); !ok || bound != minted[0] {
		t.Fatalf("map binds the owner DM to %q (%v), want %s", bound, ok, minted[0])
	}
	if tier, err := r.store.ThreadPrivacy(context.Background(), minted[0]); err != nil || tier != provider.SensitivityInternal {
		t.Fatalf("minted thread tier = %s, %v; want internal", tier, err)
	}
	if r.turns(t, minted[0]) != 1 || r.turns(t, legacyOwnerThread) != 0 {
		t.Fatal("the turn did not land on the minted thread alone")
	}
}

func TestBridgeThreadMapSurvivesRestart(t *testing.T) {
	r := newMapRig(t)
	r.deliver(t, textUpdateJSON(1, mapOwner, "private", "before restart"))
	r.deliver(t, textUpdateJSON(2, mapOwner, "private", "after restart"))
	minted := r.daemon.mintedIDs()
	appends := r.daemon.threadsOf(conversation.MethodAppendTurn)
	if len(minted) != 1 || len(appends) != 2 || appends[0] != minted[0] || appends[1] != minted[0] {
		t.Fatalf("minted %v, appends %v; want one thread carrying both turns across the restart", minted, appends)
	}
	if r.turns(t, minted[0]) != 2 {
		t.Fatalf("thread %s holds %d turns, want 2", minted[0], r.turns(t, minted[0]))
	}
}

// TestBridgeThreadMapBindConflictUsesExisting: two bridges on one store each
// take a first message for the same chat; both reach create_thread before
// either binds (a barrier holds them), so both mint. Bind lets one win; both
// appends land on the winner and the loser's thread keeps zero turns.
func TestBridgeThreadMapBindConflictUsesExisting(t *testing.T) {
	r := newMapRig(t)
	arrived, release := make(chan struct{}, 2), make(chan struct{})
	r.daemon.gate = func(method string) {
		if method == conversation.MethodCreateThread {
			arrived <- struct{}{}
			<-release
		}
	}
	rtA, pA := r.start(t, textUpdateJSON(11, mapOwner, "private", "racer a"))
	rtB, pB := r.start(t, textUpdateJSON(12, mapOwner, "private", "racer b"))
	for i := 0; i < 2; i++ {
		select {
		case <-arrived:
		case <-time.After(10 * time.Second):
			t.Fatal("both first messages never reached create_thread")
		}
	}
	close(release)
	r.await(t, pA)
	r.await(t, pB)
	for _, rt := range []*BridgeRuntime{rtA, rtB} {
		if err := rt.Stop(context.Background()); err != nil {
			t.Fatalf("stop: %v", err)
		}
	}
	winner, ok, minted := r.settled(t, mapOwner, 2)
	if !ok || len(minted) != 2 || minted[0] == minted[1] {
		t.Fatalf("minted %v, winner %q; want two mints and one binding", minted, winner)
	}
	loser := minted[0]
	if loser == winner {
		loser = minted[1]
	}
	if appends := r.daemon.threadsOf(conversation.MethodAppendTurn); len(appends) != 2 || appends[0] != winner || appends[1] != winner {
		t.Fatalf("appends %v, want both on the winner %s", appends, winner)
	}
	if r.turns(t, winner) != 2 || r.turns(t, loser) != 0 {
		t.Fatalf("winner %d turns, loser %d turns; want 2 and 0", r.turns(t, winner), r.turns(t, loser))
	}
}

// TestBridgeNeverAppendsToCallerNamedID: across first and later messages,
// every append carries an id chat.create_thread returned, never a
// prefix-derived one.
func TestBridgeNeverAppendsToCallerNamedID(t *testing.T) {
	r := newMapRig(t)
	r.deliver(t, textUpdateJSON(1, mapOwner, "private", "one"), textUpdateJSON(2, mapOwner, "private", "two"))
	r.deliver(t, textUpdateJSON(3, mapOwner, "private", "three"))
	minted := r.daemon.mintedIDs()
	appends := r.daemon.threadsOf(conversation.MethodAppendTurn)
	if len(minted) != 1 || len(appends) != 3 {
		t.Fatalf("minted %v, appends %v; want 1 mint and 3 appends", minted, appends)
	}
	for _, id := range appends {
		if id != minted[0] || strings.HasPrefix(id, "bridge-telegram:") {
			t.Fatalf("append to %q; every append must carry the minted %s", id, minted[0])
		}
	}
}

func TestBridgeThreadMapAdoptsLegacyThread(t *testing.T) {
	r := newMapRig(t)
	r.seedLegacyThread(t, true)
	r.deliver(t, textUpdateJSON(1, mapOwner, "private", "continuing"))
	if minted := r.daemon.mintedIDs(); len(minted) != 0 {
		t.Fatalf("create_thread minted %v for a chat with a legacy thread, want none", minted)
	}
	if bound, ok := r.mapped(t, mapOwner); !ok || bound != legacyOwnerThread {
		t.Fatalf("map binds %q (%v), want the legacy %s", bound, ok, legacyOwnerThread)
	}
	if n := r.turns(t, legacyOwnerThread); n != 2 {
		t.Fatalf("legacy thread holds %d turns, want 2 (its history plus the new turn)", n)
	}
}

// TestBridgeAdoptedLegacyThreadStaysRestricted: an unmarked legacy thread
// is adopted without a marker, reads restricted, refuses inbound and
// outbound mirroring, and cannot be widened afterwards.
func TestBridgeAdoptedLegacyThreadStaysRestricted(t *testing.T) {
	resetChatSender(t)
	r := newMapRig(t)
	r.seedLegacyThread(t, false)
	rt, poster := r.start(t, textUpdateJSON(1, mapOwner, "private", "secret plan"))
	r.await(t, poster)
	defer func() { _ = rt.Stop(context.Background()) }()
	ctx := context.Background()
	if bound, ok := r.mapped(t, mapOwner); !ok || bound != legacyOwnerThread {
		t.Fatalf("map binds %q (%v), want the adopted %s", bound, ok, legacyOwnerThread)
	}
	if tier, err := r.store.ThreadPrivacy(ctx, legacyOwnerThread); err != nil || tier != provider.SensitivityRestricted {
		t.Fatalf("adopted tier = %s, %v; want restricted", tier, err)
	}
	if appends := r.daemon.threadsOf(conversation.MethodAppendTurn); len(appends) != 0 {
		t.Fatalf("a restricted adopted thread recorded %v", appends)
	}
	before := len(poster.sent())
	WireChatBridge(rt, runtime.NewSystemClock())
	_, err := BridgeSend(ctx, BridgeSendParams{ThreadID: legacyOwnerThread, Content: "mirror me", Sensitivity: tools.SensitivityInternal})
	if !isRestrictedTierRefusal(err) || len(poster.sent()) != before {
		t.Fatalf("bridge.send on the adopted thread = %v with %d new sends; want the egress tier refusal and none",
			err, len(poster.sent())-before)
	}
	err = r.store.SetThreadPrivacy(ctx, legacyOwnerThread, provider.SensitivityPublic)
	if !cascade.HasKind(err, cascade.KindConflict) || err == nil || err.Error() != wantUnmarkedHasTurns {
		t.Fatalf("widening the adopted thread = %v, want KindConflict %q", err, wantUnmarkedHasTurns)
	}
}

// TestBridgeThreadMapErrorFailsClosed: a malformed map row makes every map
// read fail. Inbound: no create_thread, no append, no reply, one refusal
// record. Outbound: bridge.send and ChatBridge.Send refuse with the error
// and zero transport calls. Failing input: falling back to the chat's
// derived id appends to it.
func TestBridgeThreadMapErrorFailsClosed(t *testing.T) {
	resetChatSender(t)
	r := newMapRig(t)
	// The derived id would ALLOW, so a fallback to it would visibly append.
	if err := r.store.SetThreadPrivacy(context.Background(), legacyOwnerThread, provider.SensitivityInternal); err != nil {
		t.Fatal(err)
	}
	subject := telegram.SubjectFromToken(syntheticBotToken)
	for _, key := range []string{"threadmap/" + subject + "/chat/" + jsonInt(mapOwner), "threadmap/" + subject + "/thread/thread-x"} {
		if err := bridge.NewStore(r.db).Save(context.Background(), bridge.SubjectRow{Subject: key, TrustTier: "paired-device", AllowedFrom: []string{"1", "2"}}); err != nil {
			t.Fatalf("seed malformed row: %v", err)
		}
	}
	rt, poster := r.start(t, textUpdateJSON(1, mapOwner, "private", "do not record"))
	r.await(t, poster)
	defer func() { _ = rt.Stop(context.Background()) }()
	if len(r.daemon.calls) != 0 || len(poster.sent()) != 0 {
		t.Fatalf("map error: daemon calls %+v, sends %v; want none", r.daemon.calls, poster.sent())
	}
	if rep := rt.ChatBridge.RefusalReport(); len(rep) != 1 {
		t.Fatalf("RefusalReport = %+v, want one inbound refusal", rep)
	}
	WireChatBridge(rt, runtime.NewSystemClock())
	_, err := BridgeSend(context.Background(), BridgeSendParams{ThreadID: "thread-x", Content: "x", Sensitivity: tools.SensitivityInternal})
	if !isMapCorrupt(err) {
		t.Fatalf("bridge.send with a broken map = %v, want %v", err, cascadepa.ErrThreadMapCorrupt)
	}
	if err := rt.ChatBridge.Send(context.Background(), "thread-x", []byte("x")); !isMapCorrupt(err) || len(poster.sent()) != 0 {
		t.Fatalf("Send with a broken map = %v, %d sends; want the map error and none", err, len(poster.sent()))
	}
}

// Exact refusal texts this file asserts. The conversation and telegram
// errors are unexported, so the tests compare Kind plus the full message.
const (
	wantRefusalHead = `policy-denied: egress: class "bridge" (owner `
	wantRefusalTail = `) refused tier "restricted" (resolved "restricted"): ` +
		`the class was not registered to admit restricted content: egress: sensitivity tier not admitted by class`
	wantUnmarkedHasTurns = "conflict: conversation: thread already has turns and no privacy mode; its restricted tier cannot be changed"
	wantBindNotOwnerDM   = "policy-denied: cascade-pa/telegram: only the paired owner's direct chat can be bound to a thread"
)

// isRestrictedTierRefusal matches the bridge class's tier refusal by the
// egress sentinel's identity, its Kind and the full message. The class owner
// sits between the two text halves and is only required to be non-empty.
func isRestrictedTierRefusal(err error) bool {
	if err == nil || !errors.Is(err, egress.ErrSensitivityViolation) || !cascade.HasKind(err, cascade.KindPolicyDenied) {
		return false
	}
	msg := err.Error()
	owner, ok := strings.CutPrefix(msg, wantRefusalHead)
	if !ok {
		return false
	}
	owner, ok = strings.CutSuffix(owner, wantRefusalTail)
	return ok && owner != ""
}

func isMapCorrupt(err error) bool {
	return cascade.HasKind(err, cascade.KindIntegrity) && err.Error() == cascadepa.ErrThreadMapCorrupt.Error()
}

// TestBridgeThreadMapNeverBindsGroupChat: Bind refuses a group, a channel
// and a chat.id != from.id with nothing written, and an allowlisted owner's
// group message fed through the real dispatch leaves no map row, no thread
// and no append.
func TestBridgeThreadMapNeverBindsGroupChat(t *testing.T) {
	r := newMapRig(t)
	store, err := OpenBridgeThreadMap(context.Background(), r.db, r.deps.DataDir, r.deps.Clock)
	if err != nil {
		t.Fatal(err)
	}
	binding := cascadepa.NewBindingStore(r.deps.Clock, nil, bridgeStateAdapter{store: bridge.NewStore(r.db)})
	threads := telegram.NewThreadMap(store, binding, telegram.SubjectFromToken(syntheticBotToken))
	for _, chat := range []telegram.Chat{{ID: -100123, Type: "group"}, {ID: -100124, Type: "supergroup"},
		{ID: -100125, Type: "channel"}, {ID: mapOwner + 1, Type: "private"}} {
		_, err := threads.Bind(context.Background(), chat, mapOwner, "thread-g")
		if !cascade.HasKind(err, cascade.KindPolicyDenied) || err == nil || err.Error() != wantBindNotOwnerDM {
			t.Fatalf("Bind(%+v) = %v, want KindPolicyDenied %q", chat, err, wantBindNotOwnerDM)
		}
		if _, ok := r.mapped(t, chat.ID); ok {
			t.Fatalf("a refused Bind left a map row for %+v", chat)
		}
	}
	r.deliver(t, textUpdateJSON(1, -100123, "group", "hello group"))
	if _, ok := r.mapped(t, -100123); ok {
		t.Fatal("a group message got a mapped thread")
	}
	if len(r.daemon.calls) != 0 {
		t.Fatalf("a group message reached the daemon: %+v", r.daemon.calls)
	}
}
