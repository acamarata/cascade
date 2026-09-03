package rpc

import (
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
	// The mismatched attempt must still have consumed (deleted) the entry —
	// single-use even on a failed binding check.
	if ledger.Len() != 0 {
		t.Errorf("Len() = %d after a failed binding check, want 0 (entry must be consumed)", ledger.Len())
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
