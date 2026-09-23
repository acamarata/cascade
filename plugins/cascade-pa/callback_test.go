package cascadepa

// Purpose (this file): the R-21.210 callback-nonce store's tests, including
//   the allowed-verdict field the review found bound by nothing.
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
		Nonce: "n1", RequestID: "r1", BridgeInstance: testSubject,
		PairedSubjectID: "111", ChatID: "222", MessageID: "7",
		ExpiresAt: t0().Add(5 * time.Minute), AllowedVerdict: true,
	}
}

func claimFor(n CallbackNonce) CallbackClaim {
	return CallbackClaim{
		Nonce: n.Nonce, RequestID: n.RequestID,
		BridgeInstance: n.BridgeInstance, PairedSubjectID: n.PairedSubjectID,
		ChatID: n.ChatID, MessageID: n.MessageID,
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

// TestCallbackNonceStore_EveryBoundFieldIsCompared enumerates every field
// CallbackClaim can actually carry (T0 D1 item 2: ActionDigest and
// Verdict are no longer part of the claim — see this file's header on
// callback.go).
func TestCallbackNonceStore_EveryBoundFieldIsCompared(t *testing.T) {
	mutations := map[string]func(*CallbackClaim){
		"request id":     func(c *CallbackClaim) { c.RequestID = "other" },
		"bridge":         func(c *CallbackClaim) { c.BridgeInstance = "other" },
		"paired subject": func(c *CallbackClaim) { c.PairedSubjectID = "other" },
		"chat":           func(c *CallbackClaim) { c.ChatID = "other" },
		"message":        func(c *CallbackClaim) { c.MessageID = "other" },
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

// TestCallbackNonceStore_ConsumeReturnsStoredVerdict proves the verdict a
// redemption acts on is READ from the stored record, never asserted by a
// claim (CallbackClaim carries no Verdict field at all post-T0-D1-item-2):
// a deny-only nonce and an approve-only nonce each come back with their
// own AllowedVerdict, regardless of anything the claim could have said.
func TestCallbackNonceStore_ConsumeReturnsStoredVerdict(t *testing.T) {
	for _, want := range []bool{true, false} {
		store := NewCallbackNonceStore()
		n := testNonce()
		n.AllowedVerdict = want
		if err := store.Store(n); err != nil {
			t.Fatalf("Store: %v", err)
		}
		got, ok := store.Consume(claimFor(n), t0())
		if !ok {
			t.Fatalf("Consume(AllowedVerdict=%v) = false, want true", want)
		}
		if got.AllowedVerdict != want {
			t.Errorf("Consume returned AllowedVerdict=%v, want the stored %v", got.AllowedVerdict, want)
		}
	}
}

// TestCallbackNonceStore_MismatchLeavesNonceForRetry proves Consume MATCHES
// BEFORE it deletes (T0 D1 item 7): a mismatched tap — a stale button, a
// wrong chat, a re-rendered message — refuses without burning the nonce,
// so the SAME owner's next, correct tap still redeems it. The earlier
// design (delete first, compare after) burned the nonce on the mismatch
// itself, which permanently undecided the request over the bridge — the
// defect an adversarial CR's probe (d) found.
func TestCallbackNonceStore_MismatchLeavesNonceForRetry(t *testing.T) {
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
	got, ok := store.Consume(claimFor(n), t0())
	if !ok {
		t.Fatal("the nonce did not survive the mismatched attempt: the owner's correct retry was refused")
	}
	if got.RequestID != n.RequestID {
		t.Errorf("Consume returned %+v, want the original stored record", got)
	}
	if _, ok := store.Consume(claimFor(n), t0()); ok {
		t.Fatal("the matched consume did not actually spend the nonce: a replay was accepted")
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
