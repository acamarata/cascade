package rpc

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestNonceLedger_IssueThenConsumeSucceeds(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(1000, 0))
	ledger := NewNonceLedger(clock)

	nonce, err := ledger.Issue("vault.get", "hash1")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if nonce == "" {
		t.Fatal("Issue returned empty nonce")
	}
	if err := ledger.Consume(nonce, "vault.get", "hash1", clock.Now()); err != nil {
		t.Fatalf("Consume: %v", err)
	}
}

func TestNonceLedger_SingleUse(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(1000, 0))
	ledger := NewNonceLedger(clock)
	nonce, _ := ledger.Issue("vault.get", "hash1")

	if err := ledger.Consume(nonce, "vault.get", "hash1", clock.Now()); err != nil {
		t.Fatalf("first Consume: %v", err)
	}
	err := ledger.Consume(nonce, "vault.get", "hash1", clock.Now())
	if err == nil {
		t.Fatal("second Consume of the same nonce must fail (replay)")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Errorf("replay error kind = %v (ok=%v), want KindNotFound", kind, ok)
	}
}

func TestNonceLedger_ConcurrentDoubleConsumeYieldsExactlyOneSuccess(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(1000, 0))
	ledger := NewNonceLedger(clock)
	nonce, _ := ledger.Issue("vault.get", "hash1")

	const attempts = 50
	var successes int32
	var wg sync.WaitGroup
	var mu sync.Mutex
	wg.Add(attempts)
	for i := 0; i < attempts; i++ {
		go func() {
			defer wg.Done()
			if err := ledger.Consume(nonce, "vault.get", "hash1", clock.Now()); err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if successes != 1 {
		t.Fatalf("successes = %d, want exactly 1 under %d concurrent Consume attempts", successes, attempts)
	}
}

func TestNonceLedger_Expiry(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(1000, 0))
	ledger := NewNonceLedger(clock)
	nonce, _ := ledger.Issue("vault.get", "hash1")

	future := clock.Now().Add(nonceTTL + time.Second)
	err := ledger.Consume(nonce, "vault.get", "hash1", future)
	if err == nil {
		t.Fatal("expected expiry error")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindTimeout {
		t.Errorf("expiry error kind = %v (ok=%v), want KindTimeout", kind, ok)
	}
}

func TestNonceLedger_CrossMethodReplayRejected(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(1000, 0))
	ledger := NewNonceLedger(clock)
	nonce, _ := ledger.Issue("vault.get", "hash1")

	err := ledger.Consume(nonce, "vault.rotate", "hash1", clock.Now())
	if err == nil {
		t.Fatal("a nonce issued for method A must not validate against method B")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("cross-method error kind = %v (ok=%v), want KindInvalidInput", kind, ok)
	}
	// The mismatched attempt burns the entry: single-use even on a failed
	// binding check, and the tombstone keeps its slot until expiry.
	if ledger.Len() != 1 {
		t.Errorf("Len() = %d after a failed binding check, want 1 (a burnt tombstone)", ledger.Len())
	}
	if err := ledger.Consume(nonce, "vault.get", "hash1", clock.Now()); err == nil {
		t.Error("a burnt nonce verified a correct binding")
	}
}

func TestNonceLedger_CrossParamsReplayRejected(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(1000, 0))
	ledger := NewNonceLedger(clock)
	nonce, _ := ledger.Issue("vault.get", "hash1")

	err := ledger.Consume(nonce, "vault.get", "hash2", clock.Now())
	if err == nil {
		t.Fatal("a nonce issued for one params_hash must not validate against a different one")
	}
}

func TestNonceLedger_UnknownNonceRejected(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(1000, 0))
	ledger := NewNonceLedger(clock)
	err := ledger.Consume("never-issued", "vault.get", "hash1", clock.Now())
	if err == nil {
		t.Fatal("expected error for an unissued nonce")
	}
}

func TestNonceLedgerBounded(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(4000, 0))
	ledger := NewNonceLedger(clock)
	for i := 0; i < maxPendingNonces; i++ {
		if _, err := ledger.Issue("vault.get", "h"); err != nil {
			t.Fatalf("Issue %d: %v", i+1, err)
		}
	}
	_, err := ledger.Issue("vault.get", "h")
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Fatalf("the %dth pending Issue: err = %v, want KindUnavailable", maxPendingNonces+1, err)
	}
	if ledger.Len() != maxPendingNonces {
		t.Fatalf("a refused Issue changed the ledger: Len = %d, want %d", ledger.Len(), maxPendingNonces)
	}
	// Once every pending nonce has expired, Issue reaps them and issues again.
	clock.Advance(nonceTTL + time.Second)
	if _, err := ledger.Issue("vault.get", "h"); err != nil {
		t.Fatalf("Issue after expiry: %v", err)
	}
	if ledger.Len() != 1 {
		t.Fatalf("expired nonces not reaped on Issue: Len = %d, want 1", ledger.Len())
	}
}

// With the ledger full, attested calls carrying nonces it never issued write
// no audit record at all: anonymous appends are bounded by the ledger.
func TestAttestedCallsWithUnissuedNoncesWriteNoRecords(t *testing.T) {
	skipIfPlatformRefuses(t)
	const args = `{"target":"db-password"}`
	f := newMWFixture(t, "vault.get")
	for i := 0; i < maxPendingNonces; i++ {
		if _, err := f.ledger.Issue("vault.get", "h"); err != nil {
			t.Fatalf("filling the ledger: %v", err)
		}
	}
	before := len(f.audit.recs)
	for i := 0; i < 200; i++ {
		if _, err := f.wrapped(context.Background(), f.envelope(t, args, "not-a-nonce")); err == nil {
			t.Fatal("an attestation with an unissued nonce was accepted")
		}
	}
	if got := len(f.audit.recs) - before; got != 0 {
		t.Errorf("%d audit records written for unissued nonces, want 0", got)
	}
	if f.calls != 0 {
		t.Errorf("handler ran %d times", f.calls)
	}
}

// A pending nonce is single use even when its attestation fails: one
// challenge buys one verification record, never an unbounded retry loop.
func TestFailedVerificationBurnsTheNonce(t *testing.T) {
	skipIfPlatformRefuses(t)
	const args = `{"target":"db-password"}`
	f := newMWFixture(t, "vault.get")
	nonce := f.challenge(t, args)
	bad := f.envelope(t, `{"target":"other"}`, nonce) // signed for other params
	var env elevatedEnvelope
	_ = json.Unmarshal(bad, &env)
	env.Args = json.RawMessage(args)
	tampered, _ := json.Marshal(env)
	before := len(f.audit.recs)
	for i := 0; i < 50; i++ {
		if _, err := f.wrapped(context.Background(), tampered); err == nil {
			t.Fatal("a tampered attestation was accepted")
		}
	}
	if got := len(f.audit.recs) - before; got != 2 {
		t.Errorf("%d audit records for one burnt nonce, want exactly attempt+deny", got)
	}
	if f.ledger.Len() != 1 {
		t.Errorf("ledger holds %d entries, want the one burnt tombstone", f.ledger.Len())
	}
	if _, err := f.wrapped(context.Background(), f.envelope(t, args, nonce)); err == nil {
		t.Error("the burnt nonce verified a correct attestation")
	}
}
