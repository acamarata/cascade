// Purpose (this file): bridgeRig, the TelegramBridge test rig shared by the
//   chat*_test.go files: a testRig (rig_test.go) plus a bridge over fake
//   chat/privacy/divergence seams, a REAL chat-to-thread map
//   (cascadepa.ThreadMapStore under the real telegram ThreadMap) and a
//   switchable legacy-thread lookup.
//
// The map sits on its OWN in-memory state, separate from the binding's, so a
//   test can fail the binding store without failing the map (and the
//   reverse) and so see each gate on its own.
//
// SPORT: plugins/cascade-pa/telegram bridge test rig/TEST.

package telegram

import (
	"context"
	"strconv"
	"sync"
	"testing"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// bridgeRig is a testRig plus a TelegramBridge over its seams.
type bridgeRig struct {
	*testRig
	bridge     *TelegramBridge
	privacy    *fakeThreadPrivacy
	chat       *mintingChat
	divergence *fakeDivergenceSink
	mapState   *memBridgeState
	mapStore   *cascadepa.ThreadMapStore
	threads    ThreadMap
	legacy     *fakeLegacyThreads
}

// newBridgeRig builds the rig. Legacy threads "exist" by default, so an
// unmapped owner DM adopts its legacy id; set legacy.exists false to make
// first messages mint through CreateThread instead.
func newBridgeRig(t *testing.T) *bridgeRig {
	t.Helper()
	rig := newDefaultRig(t)
	r := &bridgeRig{testRig: rig, privacy: newFakeThreadPrivacy(), chat: &mintingChat{fakeChatService: &fakeChatService{}},
		divergence: &fakeDivergenceSink{}, mapState: newMemState(), legacy: &fakeLegacyThreads{exists: true}}
	r.mapStore = cascadepa.NewThreadMapStore(r.mapState)
	r.threads = NewThreadMap(r.mapStore, rig.stores.Binding, testSubject)
	r.bridge = NewTelegramBridge(rig.module, rig.stores.Binding, testSubject, r.seams())
	return r
}

// seams is the rig's BridgeSeams, for a test that builds a second bridge.
func (r *bridgeRig) seams() BridgeSeams {
	return BridgeSeams{Chat: r.chat, Privacy: r.privacy, Divergence: r.divergence, Threads: r.threads, Legacy: r.legacy}
}

// dispatchText runs one message through the FULL production dispatch path
// (binding, secret-scan, elevated-verb, handleInbound); senderID is decimal.
func (r *bridgeRig) dispatchText(t *testing.T, senderID string, chatID int64, text string) {
	t.Helper()
	fromID, err := strconv.ParseInt(senderID, 10, 64)
	if err != nil {
		t.Fatalf("senderID %q is not numeric: %v", senderID, err)
	}
	r.module.dispatch(context.Background(), textUpdate(1, fromID, chatID, text))
}

// pairLegacy pairs owner id as the bound sender and binds its direct chat
// to its legacy thread through the real ThreadMap, returning that thread.
func (r *bridgeRig) pairLegacy(t *testing.T, id int64) string {
	t.Helper()
	r.bind(t, strconv.FormatInt(id, 10))
	threadID, err := r.threads.Bind(context.Background(), Chat{ID: id, Type: "private"}, id, threadIDForChat(id))
	if err != nil {
		t.Fatalf("ThreadMap.Bind(%d): %v", id, err)
	}
	return threadID
}

// mintingChat is fakeChatService plus chat.create_thread: it mints
// "thread-fake-<n>" ids and records each requested privacy mode.
type mintingChat struct {
	*fakeChatService
	mintMu    sync.Mutex
	modes     []string
	createErr error
}

func (c *mintingChat) CreateThread(_ context.Context, privacyMode string) (string, error) {
	c.mintMu.Lock()
	defer c.mintMu.Unlock()
	if c.createErr != nil {
		return "", c.createErr
	}
	c.modes = append(c.modes, privacyMode)
	return "thread-fake-" + strconv.Itoa(len(c.modes)), nil
}

func (c *mintingChat) created() []string {
	c.mintMu.Lock()
	defer c.mintMu.Unlock()
	return append([]string(nil), c.modes...)
}

// fakeLegacyThreads answers LegacyThreadLookup: every canonical legacy id
// exists when exists is true; err fails every call.
type fakeLegacyThreads struct {
	mu     sync.Mutex
	exists bool
	err    error
	asked  []string
}

func (f *fakeLegacyThreads) ThreadExists(_ context.Context, threadID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, threadID)
	if f.err != nil {
		return false, f.err
	}
	_, canonical := chatIDForThread(threadID)
	return f.exists && canonical, nil
}
