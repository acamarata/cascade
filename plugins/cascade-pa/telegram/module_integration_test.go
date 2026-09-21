package telegram

// Purpose (this file): the module's lifecycle test and TestTelegramPairing, the
//   integration case this ticket's failure oracle enumerates — pairing survives
//   a restart, replays do not repeat actions, egress precedes API use, and no
//   unpaired update is dispatched — plus the R-21.210 callback nonce this ticket
//   produces for W/S-48.T4 to consume.
//
// SPORT: plugins/cascade-pa/telegram integration-tests/TEST
//   (P1-E23-W5-S48-T1).

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

func TestTelegramModule_StartStopDrain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Start is refused on Windows tier-2; module_windows_test.go proves the refusal")
	}
	rig := newDefaultRig(t)
	rig.doer.push(mustReadTestdata(t, "getupdates_text.json"), nil)
	if err := rig.module.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := rig.module.Start(context.Background()); err == nil {
		t.Fatal("starting an already-started module succeeded")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rig.module.Stop(ctx); err != nil {
		t.Fatalf("Stop did not drain: %v", err)
	}
	if err := rig.module.Stop(context.Background()); err != nil {
		t.Fatalf("Stop on a stopped module: %v", err)
	}
}

func TestTelegramCallback_NonAllowlistedAnsweredNotPaired(t *testing.T) {
	rig := newDefaultRig(t)
	called := false
	rig.module.RegisterHandler(HandlerCallbackQuery, func(context.Context, InboundMessage) error {
		called = true
		return nil
	})
	rig.module.dispatch(context.Background(), callbackUpdate(1, 999, "approve:req-1"))
	if called {
		t.Fatal("the callback handler ran for a non-allowlisted sender")
	}
	if got := lastSent(t, rig.doer); got != replyNotPaired {
		t.Fatalf("answer = %q, want %q", got, replyNotPaired)
	}
}

func TestTelegramCallback_AllowlistedDispatches(t *testing.T) {
	rig := newDefaultRig(t)
	rig.bind(t, "111")
	called := false
	rig.module.RegisterHandler(HandlerCallbackQuery, func(context.Context, InboundMessage) error {
		called = true
		return nil
	})
	rig.module.dispatch(context.Background(), callbackUpdate(1, 111, "approve:req-1"))
	if !called {
		t.Fatal("the callback handler did not run for an allowlisted sender")
	}
}

// TestTelegramPairing is the integration case the ticket's failure oracle
// names. Each subtest is one of its four clauses; each clause is its own
// function so the oracle's parts are separately named in -v output.
func TestTelegramPairing(t *testing.T) {
	t.Run("no unpaired dispatch", pairingNoUnpairedDispatch)
	t.Run("pairing identity survives restart", pairingSurvivesRestart)
	t.Run("replays do not repeat actions", pairingReplaysDoNotRepeat)
	t.Run("egress precedes API use", pairingEgressPrecedesAPIUse)
}

func pairingNoUnpairedDispatch(t *testing.T) {
	rig := newDefaultRig(t)
	ran := false
	rig.module.RegisterHandler(HandlerText, func(context.Context, InboundMessage) error {
		ran = true
		return nil
	})
	rig.module.dispatch(context.Background(), textUpdate(1, 111, 111, "hello"))
	if ran {
		t.Fatal("an unpaired bot dispatched to a handler")
	}
}

func pairingSurvivesRestart(t *testing.T) {
	state := newMemState()
	clock := fixedTestClock{t0()}
	ctx := context.Background()

	// First process: issue and redeem a code.
	first := cascadepa.NewStores(clock, &okRegistrar{}, state, testPairKey(t))
	code, err := first.Pairing.IssueCode(ctx, fixedEntropy(), testSubject)
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}
	doer := &fakeDoer{}
	client := NewBotClient(testSubject, doer, &tierGate{}, first.Updates)
	pairer := newPairCoordinator(first.Pairing, first.Binding, nil, clock)
	module := NewTelegramModule(testSubject, client, first.Binding, pairer, nodeVerbPolicy(), clock)
	// This test predates T0 D1 (P1-E23-W5-S48-T3): it constructs the module
	// directly rather than through the rig, so it needs the SAME permissive
	// default rig_test.go installs, or the unwired fail-closed scanner would
	// refuse the "/pair <code>" text this test dispatches below.
	module.secretScanner = fakeSecretScanner{}
	module.dispatch(ctx, textUpdate(1, 111, 111, "/pair "+code))
	if got := lastSent(t, doer); !strings.HasPrefix(got, replyPaired) {
		t.Fatalf("pairing reply = %q, want the %q confirmation", got, replyPaired)
	}

	// Second process over the SAME durable state: the binding is still there.
	second := cascadepa.NewStores(clock, &okRegistrar{}, state, testPairKey(t))
	allowed, err := second.Binding.IsAllowed(ctx, testSubject, "111")
	if err != nil {
		t.Fatalf("IsAllowed after restart: %v", err)
	}
	if !allowed {
		t.Fatal("the pairing did not survive a restart")
	}
}

func pairingReplaysDoNotRepeat(t *testing.T) {
	state := newMemState()
	ledger := cascadepa.NewUpdateLedger(state)
	ctx := context.Background()
	fresh, err := ledger.Accept(ctx, testSubject, 900000001)
	if err != nil || !fresh {
		t.Fatalf("first Accept = (%v, %v), want (true, nil)", fresh, err)
	}
	again, err := ledger.Accept(ctx, testSubject, 900000001)
	if err != nil {
		t.Fatalf("second Accept: %v", err)
	}
	if again {
		t.Fatal("a replayed update id was reported as new")
	}
}

func pairingEgressPrecedesAPIUse(t *testing.T) {
	doer := &fakeDoer{}
	gate := &tierGate{refuse: errTestGateClosed}
	c := newTestClient(doer, gate, newMemState())
	if got := pollBriefly(t, c); len(got) != 0 {
		t.Fatalf("delivered %d updates through a closed gate", len(got))
	}
	if len(doer.methods()) != 0 {
		t.Fatalf("the transport was reached through a closed gate: %v", doer.methods())
	}
	if len(gate.observedTiers()) == 0 {
		t.Fatal("the gate was never consulted")
	}
}

// TestCallbackNonceBinding proves the R-21.210 store this ticket produces and
// W/S-48.T4 consumes, INCLUDING the verdict field the review found unbound.
func TestCallbackNonceBinding(t *testing.T) {
	store := cascadepa.NewCallbackNonceStore()
	nonce := cascadepa.CallbackNonce{
		Nonce: "n1", RequestID: "r1", ActionDigest: "d1", BridgeInstance: testSubject,
		PairedSubjectID: "111", ChatID: "111", MessageID: "7",
		ExpiresAt: t0().Add(5 * time.Minute), AllowedVerdict: true,
	}
	if err := store.Store(nonce); err != nil {
		t.Fatalf("Store: %v", err)
	}
	claim := cascadepa.CallbackClaim{
		Nonce: "n1", RequestID: "r1", ActionDigest: "d1", BridgeInstance: testSubject,
		PairedSubjectID: "111", ChatID: "111", MessageID: "7", Verdict: true,
	}
	if _, ok := store.Consume(claim, t0()); !ok {
		t.Fatal("Consume(exact match) = false")
	}
	if _, ok := store.Consume(claim, t0()); ok {
		t.Fatal("a nonce was consumed twice")
	}

	// The bypass the review found: a nonce minted for verdict=false consumed
	// by a callback claiming approval.
	denyOnly := nonce
	denyOnly.Nonce, denyOnly.AllowedVerdict = "n2", false
	if err := store.Store(denyOnly); err != nil {
		t.Fatalf("Store: %v", err)
	}
	approveClaim := claim
	approveClaim.Nonce, approveClaim.Verdict = "n2", true
	if _, ok := store.Consume(approveClaim, t0()); ok {
		t.Fatal("a deny-only nonce authorised an approval")
	}
}

// rewritingGate replaces EVERY outbound body with a fixed marker. It is how a
// test can tell "this reply crossed the firewall" from "this reply happened to
// be the same bytes either way": if a send path posts its own text instead of
// the gate's return value, the marker is missing and the assertion fails.
type rewritingGate struct {
	mu   sync.Mutex
	seen []string
}

// gateMarker is the only text a caller that respects the gate can post.
const gateMarker = "REWRITTEN-BY-THE-FIREWALL"

func (g *rewritingGate) Guard(
	_ context.Context, tier cascadepa.SensitivityTier, content []byte,
) ([]byte, error) {
	g.mu.Lock()
	g.seen = append(g.seen, string(content))
	g.mu.Unlock()
	if tier != cascadepa.TierInternal && tier != cascadepa.TierPublic {
		return nil, errTestGateClosed
	}
	return []byte(gateMarker), nil
}

func (g *rewritingGate) observed() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.seen...)
}

// TestPairingRepliesAreTheGatesOutput is the defence for the PAIRING path
// specifically (the poll path already had one). Every reply the pairing flow
// produces — the confirmation and the refusal — must be what the egress gate
// RETURNED, so a say() that called the transport directly goes red here.
func TestPairingRepliesAreTheGatesOutput(t *testing.T) {
	ctx := context.Background()
	clock := fixedTestClock{t0()}
	state := newMemState()
	stores := cascadepa.NewStores(clock, &okRegistrar{}, state, testPairKey(t))
	code, err := stores.Pairing.IssueCode(ctx, fixedEntropy(), testSubject)
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}
	doer := &fakeDoer{}
	gate := &rewritingGate{}
	client := NewBotClient(testSubject, doer, gate, stores.Updates)
	pairer := newPairCoordinator(stores.Pairing, stores.Binding, &recordingSink{}, clock)
	module := NewTelegramModule(testSubject, client, stores.Binding, pairer, nodeVerbPolicy(), clock)
	// See pairingSurvivesRestart's identical comment: a direct construction
	// needs the rig's permissive default so T0 D1's unwired fail-closed
	// scanner does not refuse these (non-credential-shaped) pairing replies
	// before the egress gate this test asserts on ever sees them.
	module.secretScanner = fakeSecretScanner{}

	// A wrong candidate (the refusal reply) and then the right one (the
	// confirmation reply): both go out through the pairing path's say().
	module.dispatch(ctx, textUpdate(1, 111, 999, "/pair 00000000"))
	module.dispatch(ctx, textUpdate(2, 111, 999, "/pair "+code))

	sent := doer.sentTexts()
	if len(sent) != 2 {
		t.Fatalf("the pairing flow sent %d replies, want 2: %v", len(sent), sent)
	}
	for i, got := range sent {
		if got != gateMarker {
			t.Fatalf("pairing reply %d reached the transport as %q, not the firewall's output; "+
				"this send path bypasses the egress gate", i, got)
		}
	}
	// And the gate really saw the pairing flow's own texts, so the assertion
	// above is about routing rather than about a gate that saw nothing.
	observed := gate.observed()
	var sawFailed, sawPaired bool
	for _, body := range observed {
		switch body {
		case replyPairingFailed:
			sawFailed = true
		case replyPaired + testSubject:
			sawPaired = true
		}
	}
	if !sawFailed || !sawPaired {
		t.Fatalf("the gate did not see both pairing replies: %v", observed)
	}
	if allowed, _ := stores.Binding.IsAllowed(ctx, testSubject, "111"); !allowed {
		t.Fatal("the correct code did not bind the sender")
	}
}
