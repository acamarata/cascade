// Purpose: AuthorizeWithExpiry reports the deadline the ledger enforces,
// and refuses what Authorize refuses.
//
// SPORT: internal/policy SameTurnLedger.AuthorizeWithExpiry/ADDED (P1-SEC-10).
package policy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestAuthorizeWithExpiryMatchesWindow pins the reported expiry to the
// clock plus the two-minute window and to the expiry actually stored, and
// proves the entry stops answering at that instant.
func TestAuthorizeWithExpiryMatchesWindow(t *testing.T) {
	ctx := context.Background()
	ledger, clock := newLedger(t)
	subject := testSubject()

	nonce, expiresAt, err := ledger.AuthorizeWithExpiry(ctx, subject, "ls /srv")
	if err != nil || nonce == "" {
		t.Fatalf("AuthorizeWithExpiry = %q, %v", nonce, err)
	}
	if want := clock.Now().Add(2 * time.Minute); !expiresAt.Equal(want) {
		t.Fatalf("expiresAt = %v, want %v", expiresAt, want)
	}
	ledger.mu.Lock()
	entry, ok := ledger.entries[sameTurnKey(subject, "ls /srv")]
	ledger.mu.Unlock()
	if !ok || !entry.exp.Equal(expiresAt) || entry.nonce != nonce {
		t.Fatalf("stored entry = %+v (present %t), want exp %v nonce %q", entry, ok, expiresAt, nonce)
	}

	clock.Set(expiresAt)
	if ok, err := ledger.Authorized(ctx, subject, "ls /srv"); err != nil || ok {
		t.Fatalf("the entry answered at its expiry: %v, %v", ok, err)
	}
}

// TestAuthorizeWithExpiryRefusesLikeAuthorize covers the refusals: an empty
// action, an elevation-class verb and a duplicate each store nothing and
// report a zero expiry.
func TestAuthorizeWithExpiryRefusesLikeAuthorize(t *testing.T) {
	ctx := context.Background()
	ledger, _ := newLedger(t)
	subject := testSubject()

	if _, exp, err := ledger.AuthorizeWithExpiry(ctx, subject, ""); !cascade.HasKind(err, cascade.KindInvalidInput) || !exp.IsZero() {
		t.Fatalf("empty action = %v, %v", exp, err)
	}
	if _, exp, err := ledger.AuthorizeWithExpiry(ctx, subject, "vault.get"); !errors.Is(err, ErrSameTurnElevation) || !exp.IsZero() {
		t.Fatalf("elevation-class action = %v, %v", exp, err)
	}
	if ledger.Live() != 0 {
		t.Fatalf("Live = %d after refusals, want 0", ledger.Live())
	}
	if _, _, err := ledger.AuthorizeWithExpiry(ctx, subject, "ls /srv"); err != nil {
		t.Fatalf("first authorization: %v", err)
	}
	if _, exp, err := ledger.AuthorizeWithExpiry(ctx, subject, "ls /srv"); !errors.Is(err, ErrSameTurnDuplicate) || !exp.IsZero() {
		t.Fatalf("duplicate = %v, %v", exp, err)
	}
}
