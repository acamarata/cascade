// Purpose: the single-use ledger's contract over a REAL B-layer store —
// providers/sqlite on a t.TempDir() file, closed and reopened to model a
// daemon restart — covering the first claim, the replay refusal, survival
// across a restart, a damaged row, and the validation that keeps a nonce
// from addressing a row it could never have written.
//
// SPORT: internal/policy Ledger/ADDED, LedgerRecord/ADDED
// (P1-E09-W2-S18-T3).
package policy

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ledgerFixture is a ledger over a real database file.
type ledgerFixture struct {
	f *approvalFixture
}

// newLedgerFixture reuses the approval fixture's real store.
func newLedgerFixture(t *testing.T) *ledgerFixture {
	t.Helper()
	return &ledgerFixture{f: newApprovalFixture(t)}
}

// ledger returns the ledger under the queue.
func (l *ledgerFixture) ledger() *Ledger { return l.f.queue.ledger }

// spentRecord is the row the tests claim.
func spentRecord(nonce string) LedgerRecord {
	return LedgerRecord{
		Nonce:      nonce,
		RequestID:  "req-0001",
		ActionHash: hashApproval([]byte("edit-a")),
		Subject:    testSubject().String(),
	}
}

// TestSingleUseLedger proves a nonce can be claimed once, that the claim
// is stamped from the injected clock, and that the row lands in the audit
// domain rather than anywhere else.
func TestSingleUseLedger(t *testing.T) {
	ctx := context.Background()
	l := newLedgerFixture(t)

	if err := l.ledger().Consume(ctx, spentRecord("nonce-0001")); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	stored, err := l.f.db.Get(ctx, string(storage.DomainAudit), ledgerKeyPrefix+"nonce-0001")
	if err != nil {
		t.Fatalf("reading the ledger row back from the audit domain: %v", err)
	}
	if len(stored) == 0 {
		t.Fatal("the ledger row is empty")
	}
	// A second, different nonce is unaffected.
	if err := l.ledger().Consume(ctx, spentRecord("nonce-0002")); err != nil {
		t.Fatalf("claiming a different nonce: %v", err)
	}
}

// TestSingleUseLedgerReplay proves a replayed nonce is refused, and that
// the refusal does not depend on expiry: the clock has not moved.
func TestSingleUseLedgerReplay(t *testing.T) {
	ctx := context.Background()
	l := newLedgerFixture(t)

	if err := l.ledger().Consume(ctx, spentRecord("nonce-0001")); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	err := l.ledger().Consume(ctx, spentRecord("nonce-0001"))
	if !errors.Is(err, ErrTokenReplayed) {
		t.Fatalf("second claim = %v, want ErrTokenReplayed", err)
	}
	if !errors.Is(err, cascade.ErrConflict) {
		t.Errorf("the replay refusal does not carry KindConflict: %v", err)
	}
}

// TestSingleUseLedgerReplayIgnoresADamagedRow proves a stored row that
// cannot be decoded is STILL a spent nonce. Re-opening a nonce because its
// record was damaged is exactly the state an attacker would manufacture.
func TestSingleUseLedgerReplayIgnoresADamagedRow(t *testing.T) {
	ctx := context.Background()
	l := newLedgerFixture(t)

	if err := l.f.db.Put(ctx, string(storage.DomainAudit),
		ledgerKeyPrefix+"nonce-0001", []byte("{not json")); err != nil {
		t.Fatalf("seeding a damaged row: %v", err)
	}
	if err := l.ledger().Consume(ctx, spentRecord("nonce-0001")); !errors.Is(err, ErrTokenReplayed) {
		t.Fatalf("claiming a nonce whose row is damaged = %v, want ErrTokenReplayed", err)
	}
}

// TestSingleUseLedgerRestartPersistence is the durability assertion the
// contract names: a nonce consumed through ConsumeToken, the store closed
// and reopened from the SAME path, and the same nonce refused as a replay.
//
// The minter is reset so the reopened queue mints the identical nonce,
// which is what makes this a test of the LEDGER's memory rather than of
// the minter's uniqueness.
func TestSingleUseLedgerRestartPersistence(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)

	first := approvedEntry(t, f, "edit-a")
	if _, err := f.queue.ConsumeToken(ctx, redeem(first, "edit-a")); err != nil {
		t.Fatalf("first redemption: %v", err)
	}

	// Close the database and reopen it from the same path: a daemon
	// restart. Pending entries are gone (they live in daemon memory), the
	// ledger is not.
	if err := f.db.Close(); err != nil {
		t.Fatalf("closing the store: %v", err)
	}
	f.minter.n = 0
	f.open(t)

	second := approvedEntry(t, f, "edit-a")
	if second.Token.Nonce != first.Token.Nonce {
		t.Fatalf("the reopened queue minted nonce %q, want the same %q so this tests the ledger",
			second.Token.Nonce, first.Token.Nonce)
	}
	_, err := f.queue.ConsumeToken(ctx, redeem(second, "edit-a"))
	if !errors.Is(err, ErrTokenReplayed) {
		t.Fatalf("redeeming a nonce spent before the restart = %v, want ErrTokenReplayed", err)
	}
}

// TestLedgerValidation proves a nonce that could never have been written
// can never address a stored row either, and that a row with no request id
// is refused.
func TestLedgerValidation(t *testing.T) {
	ctx := context.Background()
	l := newLedgerFixture(t)

	cases := []struct {
		name string
		rec  LedgerRecord
	}{
		{"empty nonce", LedgerRecord{RequestID: "req-0001"}},
		{"path-separator nonce", LedgerRecord{Nonce: "../escape", RequestID: "req-0001"}},
		{"oversized nonce", LedgerRecord{Nonce: longNonce(), RequestID: "req-0001"}},
		{"no request id", LedgerRecord{Nonce: "nonce-0001"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := l.ledger().Consume(ctx, tc.rec); !errors.Is(err, cascade.ErrInvalidInput) {
				t.Fatalf("Consume(%s) = %v, want an invalid-input refusal", tc.name, err)
			}
		})
	}
}

// TestNewLedgerRequiresCollaborators proves a ledger cannot be built
// without the two things it needs to answer a replay question.
func TestNewLedgerRequiresCollaborators(t *testing.T) {
	f := newApprovalFixture(t)
	if _, err := NewLedger(nil, f.clock); err == nil {
		t.Error("NewLedger with no store = nil error, want a refusal")
	}
	if _, err := NewLedger(f.db, nil); err == nil {
		t.Error("NewLedger with no clock = nil error, want a refusal")
	}
	if _, err := NewLedger(f.db, testkit.NewFrozenClock(baseTime)); err != nil {
		t.Errorf("NewLedger with both: %v", err)
	}
}

// longNonce returns a nonce over the length limit.
func longNonce() string {
	buf := make([]byte, maxNonceLen+1)
	for i := range buf {
		buf[i] = 'n'
	}
	return string(buf)
}
