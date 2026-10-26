package rpc

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// Recycling failed nonces cannot lift the bound: with the clock frozen, a
// challenge followed by a bogus attestation per loop stops at the ledger cap
// and writes at most three records per slot.
func TestBurntNoncesKeepTheirSlotsSoAuditAppendsStayBounded(t *testing.T) {
	skipIfPlatformRefuses(t)
	const args = `{"target":"db-password"}`
	f := newMWFixture(t, "vault.get")
	refused := 0
	for i := 0; i < 3*maxPendingNonces; i++ {
		_, err := f.wrapped(context.Background(), json.RawMessage(args))
		eo, ok := err.(*ErrorObject)
		if !ok {
			t.Fatalf("loop %d: err = %#v", i, err)
		}
		nonce, ok := eo.Data.(elevationRequiredData)
		if !ok {
			refused++ // Issue refused: the ledger is full
			continue
		}
		bad := f.envelope(t, args, nonce.Nonce)
		var env elevatedEnvelope
		_ = json.Unmarshal(bad, &env)
		env.Args = json.RawMessage(`{"target":"other"}`)
		tampered, _ := json.Marshal(env)
		if _, err := f.wrapped(context.Background(), tampered); err == nil {
			t.Fatal("a tampered attestation was accepted")
		}
	}
	if refused == 0 {
		t.Fatal("the ledger never refused a challenge: burnt nonces freed their slots")
	}
	if got, limit := len(f.audit.recs), 3*maxPendingNonces; got > limit {
		t.Errorf("%d audit records with the clock frozen, want at most %d", got, limit)
	}
	if f.ledger.Len() != maxPendingNonces {
		t.Errorf("ledger Len = %d, want the cap %d", f.ledger.Len(), maxPendingNonces)
	}
}

// Concurrent attested calls on one pending nonce: exactly one reaches the
// audit log, the rest are refused with no record.
func TestConcurrentAttestedCallsOnOneNonceWriteOneVerification(t *testing.T) {
	skipIfPlatformRefuses(t)
	const args = `{"target":"db-password"}`
	f := newMWFixture(t, "vault.get")
	nonce := f.challenge(t, args)
	bad := f.envelope(t, args, nonce)
	var env elevatedEnvelope
	_ = json.Unmarshal(bad, &env)
	env.Args = json.RawMessage(`{"target":"other"}`)
	tampered, _ := json.Marshal(env)
	before := len(f.audit.phases())
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = f.wrapped(context.Background(), tampered)
		}()
	}
	wg.Wait()
	if got := len(f.audit.phases()) - before; got != 2 {
		t.Errorf("%d audit records for 64 concurrent calls on one nonce, want attempt+deny", got)
	}
}

// claim is exclusive: a second claim is refused until release, and a burnt
// nonce can never be claimed or consumed again.
func TestNonceLedgerClaimIsExclusive(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(1000, 0))
	ledger := NewNonceLedger(clock)
	nonce, _ := ledger.Issue("vault.get", "h")
	if err := ledger.claim(nonce); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := ledger.claim(nonce); err == nil {
		t.Fatal("a second claim on a claimed nonce succeeded")
	}
	ledger.release(nonce)
	if err := ledger.claim(nonce); err != nil {
		t.Fatalf("claim after release: %v", err)
	}
	ledger.burn(nonce)
	if err := ledger.claim(nonce); err == nil {
		t.Fatal("a burnt nonce was claimed")
	}
	ledger.release(nonce)
	if err := ledger.claim(nonce); err == nil {
		t.Fatal("release revived a burnt nonce")
	}
	if err := ledger.Consume(nonce, "vault.get", "h", clock.Now()); err == nil {
		t.Fatal("a burnt nonce was consumed")
	}
	if ledger.Len() != 1 {
		t.Errorf("Len = %d, want the tombstone to hold its slot", ledger.Len())
	}
}

// A call that refuses before verifying (the attempt record could not be
// appended) releases its claim: a valid retry on the same nonce still works.
func TestFailedAttemptAppendReleasesTheClaimSoARetrySucceeds(t *testing.T) {
	skipIfPlatformRefuses(t)
	const args = `{"target":"db-password"}`
	f := newMWFixture(t, "vault.get")
	nonce := f.challenge(t, args)
	f.audit.fails[ElevationPhaseAttempt] = true
	if _, err := f.wrapped(context.Background(), f.envelope(t, args, nonce)); err == nil {
		t.Fatal("an attested call succeeded while the audit store was down")
	}
	f.audit.fails[ElevationPhaseAttempt] = false
	if _, err := f.wrapped(context.Background(), f.envelope(t, args, nonce)); err != nil || f.calls != 1 {
		t.Fatalf("retry after a failed attempt append: err=%v calls=%d", err, f.calls)
	}
}

// Claimed and burnt entries expire like any other (a tombstone that never
// left would lock every elevated verb out until a restart), and a successful
// Consume frees its slot at once.
func TestClaimedAndBurntEntriesAreReapedAtExpiryAndSuccessFreesItsSlot(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(1000, 0))
	l := NewNonceLedger(clock)
	claimed, _ := l.Issue("m", "h")
	burnt, _ := l.Issue("m", "h")
	if err := l.claim(claimed); err != nil {
		t.Fatal(err)
	}
	l.burn(burnt)
	if l.Len() != 2 {
		t.Fatalf("Len = %d, want the claimed entry and the tombstone", l.Len())
	}
	clock.Advance(nonceTTL + time.Second)
	granted, err := l.Issue("m", "h")
	if err != nil || l.Len() != 1 {
		t.Fatalf("claimed/burnt entries not reaped at expiry: len=%d err=%v", l.Len(), err)
	}
	if err := l.claim(granted); err != nil {
		t.Fatal(err)
	}
	if err := l.Consume(granted, "m", "h", clock.Now()); err != nil || l.Len() != 0 {
		t.Fatalf("a granted nonce kept its slot: len=%d err=%v", l.Len(), err)
	}
	l.release(granted) // inert on a consumed nonce
	if l.Len() != 0 {
		t.Fatalf("release revived a consumed nonce: len=%d", l.Len())
	}
}
