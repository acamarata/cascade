package telegram

// Purpose (this file): testRig — one fully wired TelegramModule over the fakes
//   in helpers_test.go, so a dispatch-gate test is three lines rather than
//   twelve and every test in this package wires the module the same way.
//
// SPORT: plugins/cascade-pa/telegram test-rig/TEST (P1-E23-W5-S48-T1).

import (
	"context"
	"sync"
	"testing"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// testSubject is the bridge instance id every test in this package pairs.
const testSubject = "tg-testsubject00"

// testRig is one fully wired module plus the fakes behind it.
type testRig struct {
	module *TelegramModule
	doer   *fakeDoer
	gate   *tierGate
	state  *memBridgeState
	stores *cascadepa.Stores
	sink   *recordingSink
}

// recordingSink captures every lockout event.
type recordingSink struct {
	mu     sync.Mutex
	events []LockoutEvent
}

func (s *recordingSink) EmitLockout(_ context.Context, e LockoutEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

func (s *recordingSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

// newRig builds a module over the in-memory fakes with the given clock,
// registrar and elevation policy.
func newRig(t *testing.T, clock cascadepa.PairClock, reg cascadepa.DeviceRegistrar,
	policy cascadepa.ElevationPolicy) *testRig {
	t.Helper()
	state := newMemState()
	stores := cascadepa.NewStores(clock, reg, state, testPairKey(t))
	doer := &fakeDoer{}
	gate := &tierGate{}
	client := NewBotClient(testSubject, doer, gate, stores.Updates)
	sleep, _ := instantSleep()
	client.sleep = sleep
	sink := &recordingSink{}
	pairer := newPairCoordinator(stores.Pairing, stores.Binding, sink, clock)
	module := NewTelegramModule(testSubject, client, stores.Binding, pairer, policy)
	return &testRig{module: module, doer: doer, gate: gate, state: state, stores: stores, sink: sink}
}

// newDefaultRig is newRig with the usual choices: a frozen clock, a recording
// registrar and the node-verb elevation policy.
func newDefaultRig(t *testing.T) *testRig {
	t.Helper()
	return newRig(t, fixedTestClock{t0()}, &okRegistrar{}, nodeVerbPolicy())
}

// bind pairs senderID on the rig's subject.
func (r *testRig) bind(t *testing.T, senderID string) {
	t.Helper()
	if _, err := r.stores.Binding.Bind(context.Background(), testSubject, senderID); err != nil {
		t.Fatalf("Bind(%s): %v", senderID, err)
	}
}

// allows reports whether senderID is on the rig subject's allowlist.
func (r *testRig) allows(t *testing.T, senderID string) bool {
	t.Helper()
	ok, err := r.stores.Binding.IsAllowed(context.Background(), testSubject, senderID)
	if err != nil {
		t.Fatalf("IsAllowed: %v", err)
	}
	return ok
}
