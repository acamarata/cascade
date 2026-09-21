package cascadepa

// Purpose (this file): the R-21.210 callback-nonce store's tests, including the
//   eighth field (the allowed verdict) the review found bound by nothing.
//
// SPORT: plugins/cascade-pa callback-tests/TEST (P1-E23-W5-S48-T1).

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func testNonce() CallbackNonce {
	return CallbackNonce{
		Nonce: "n1", RequestID: "r1", ActionDigest: "d1", BridgeInstance: testSubject,
		PairedSubjectID: "111", ChatID: "222", MessageID: "7",
		ExpiresAt: t0().Add(5 * time.Minute), AllowedVerdict: true,
	}
}

func claimFor(n CallbackNonce) CallbackClaim {
	return CallbackClaim{
		Nonce: n.Nonce, RequestID: n.RequestID, ActionDigest: n.ActionDigest,
		BridgeInstance: n.BridgeInstance, PairedSubjectID: n.PairedSubjectID,
		ChatID: n.ChatID, MessageID: n.MessageID, Verdict: n.AllowedVerdict,
	}
}

func TestCallbackNonceStore_StoreThenConsumeExactMatch(t *testing.T) {
	store := NewCallbackNonceStore()
	n := testNonce()
	if err := store.Store(n); err != nil {
		t.Fatalf("Store: %v", err)
	}
	got, ok := store.Consume(claimFor(n), t0())
	if !ok {
		t.Fatal("Consume(exact match) = false")
	}
	if got.RequestID != n.RequestID || got.AllowedVerdict != n.AllowedVerdict {
		t.Fatalf("Consume returned %+v, want the stored record", got)
	}
}

func TestCallbackNonceStore_ConsumeIsOneUse(t *testing.T) {
	store := NewCallbackNonceStore()
	n := testNonce()
	if err := store.Store(n); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, ok := store.Consume(claimFor(n), t0()); !ok {
		t.Fatal("first Consume = false")
	}
	if _, ok := store.Consume(claimFor(n), t0()); ok {
		t.Fatal("a nonce was redeemed twice")
	}
}

// TestCallbackNonceStore_EveryBoundFieldIsCompared enumerates all eight
// R-21.210 fields. The verdict row is the bypass the review found.
func TestCallbackNonceStore_EveryBoundFieldIsCompared(t *testing.T) {
	mutations := map[string]func(*CallbackClaim){
		"request id":     func(c *CallbackClaim) { c.RequestID = "other" },
		"action digest":  func(c *CallbackClaim) { c.ActionDigest = "other" },
		"bridge":         func(c *CallbackClaim) { c.BridgeInstance = "other" },
		"paired subject": func(c *CallbackClaim) { c.PairedSubjectID = "other" },
		"chat":           func(c *CallbackClaim) { c.ChatID = "other" },
		"message":        func(c *CallbackClaim) { c.MessageID = "other" },
		"verdict":        func(c *CallbackClaim) { c.Verdict = !c.Verdict },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			store := NewCallbackNonceStore()
			n := testNonce()
			if err := store.Store(n); err != nil {
				t.Fatalf("Store: %v", err)
			}
			claim := claimFor(n)
			mutate(&claim)
			if _, ok := store.Consume(claim, t0()); ok {
				t.Fatalf("a claim with a mismatched %s was accepted", name)
			}
		})
	}
}

// TestCallbackNonceStore_DenyOnlyNonceCannotApprove is the same bypass stated
// as the attack: a prompt minted for "deny" must not authorise an approval.
func TestCallbackNonceStore_DenyOnlyNonceCannotApprove(t *testing.T) {
	store := NewCallbackNonceStore()
	n := testNonce()
	n.AllowedVerdict = false
	if err := store.Store(n); err != nil {
		t.Fatalf("Store: %v", err)
	}
	claim := claimFor(n)
	claim.Verdict = true
	if _, ok := store.Consume(claim, t0()); ok {
		t.Fatal("a deny-only nonce authorised an approval")
	}
}

// TestCallbackNonceStore_ANonceIsSpentByBeingChecked: a wrong-field attempt
// must still consume the record, or an attacker grinds the fields one at a
// time.
func TestCallbackNonceStore_ANonceIsSpentByBeingChecked(t *testing.T) {
	store := NewCallbackNonceStore()
	n := testNonce()
	if err := store.Store(n); err != nil {
		t.Fatalf("Store: %v", err)
	}
	wrong := claimFor(n)
	wrong.ChatID = "other"
	if _, ok := store.Consume(wrong, t0()); ok {
		t.Fatal("a mismatched claim was accepted")
	}
	if _, ok := store.Consume(claimFor(n), t0()); ok {
		t.Fatal("the nonce survived a failed attempt and was then redeemed")
	}
}

func TestCallbackNonceStore_ExpiredRefused(t *testing.T) {
	store := NewCallbackNonceStore()
	n := testNonce()
	if err := store.Store(n); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, ok := store.Consume(claimFor(n), n.ExpiresAt.Add(time.Second)); ok {
		t.Fatal("an expired nonce was redeemed")
	}
}

func TestCallbackNonceStore_UnknownNonceRefused(t *testing.T) {
	store := NewCallbackNonceStore()
	if _, ok := store.Consume(CallbackClaim{Nonce: "never-stored"}, t0()); ok {
		t.Fatal("an unknown nonce was redeemed")
	}
}

func TestCallbackNonceStore_DuplicateStoreRefused(t *testing.T) {
	store := NewCallbackNonceStore()
	n := testNonce()
	if err := store.Store(n); err != nil {
		t.Fatalf("Store: %v", err)
	}
	err := store.Store(n)
	if err == nil {
		t.Fatal("a duplicate nonce value was stored")
	}
	if !strings.Contains(err.Error(), "already stored") {
		t.Fatalf("got %v, want the duplicate refusal", err)
	}
}

func TestCallbackNonceStore_EmptyNonceRefused(t *testing.T) {
	if err := NewCallbackNonceStore().Store(CallbackNonce{}); err == nil {
		t.Fatal("an empty nonce value was stored")
	}
}

// TestCallbackNonceStore_ConcurrentConsumeAdmitsOneWinner proves the one-use
// guarantee under a race, counting winners rather than only checking that the
// test finished.
func TestCallbackNonceStore_ConcurrentConsumeAdmitsOneWinner(t *testing.T) {
	store := NewCallbackNonceStore()
	n := testNonce()
	if err := store.Store(n); err != nil {
		t.Fatalf("Store: %v", err)
	}
	const racers = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners int
		start   = make(chan struct{})
	)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, ok := store.Consume(claimFor(n), t0())
			mu.Lock()
			if ok {
				winners++
			}
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()
	if winners != 1 {
		t.Fatalf("%d goroutines redeemed one nonce, want exactly 1", winners)
	}
}
