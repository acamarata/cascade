package nodes

import (
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestPairingCodeEntropyAndTTL(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store := NewPairingStore(clock)

	code, err := store.GeneratePairingCode(strings.NewReader(strings.Repeat("x", 64)), "issuer-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(code) != 26 {
		t.Fatalf("expected a 26-character code, got %d: %q", len(code), code)
	}
	raw, derr := DecodePairingCode(code)
	if derr != nil {
		t.Fatalf("generated code failed its own shape check: %v", derr)
	}
	if len(raw) != 16 {
		t.Fatalf("expected 128 bits (16 bytes), got %d", len(raw))
	}

	// TTL: valid at issuance, refused once PairingCodeTTL has elapsed.
	if _, err := store.VerifyPairingCode("peer-a", code); err != nil {
		t.Fatalf("expected the fresh code to verify: %v", err)
	}
}

func TestPairingCodeAtomicSingleUse(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store := NewPairingStore(clock)
	code, err := store.GeneratePairingCode(strings.NewReader(strings.Repeat("y", 64)), "issuer-1")
	if err != nil {
		t.Fatal(err)
	}

	issuer, err := store.VerifyPairingCode("peer-a", code)
	if err != nil {
		t.Fatalf("first verification should succeed: %v", err)
	}
	if issuer != "issuer-1" {
		t.Fatalf("issuer = %q, want issuer-1", issuer)
	}

	if _, err := store.VerifyPairingCode("peer-a", code); err == nil {
		t.Fatal("expected the second verification of the same code to be refused")
	}
	if _, err := store.VerifyPairingCode("peer-b", code); err == nil {
		t.Fatal("expected a DIFFERENT peer's verification of an already-consumed code to be refused too")
	}
}

func TestPairingCodeExpiryRefused(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store := NewPairingStore(clock)
	code, err := store.GeneratePairingCode(strings.NewReader(strings.Repeat("z", 64)), "issuer-1")
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(PairingCodeTTL + time.Second)

	if _, err := store.VerifyPairingCode("peer-a", code); err == nil {
		t.Fatal("expected an expired code to be refused")
	}
}

func TestPairingPeerLockout(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store := NewPairingStore(clock)
	code, err := store.GeneratePairingCode(strings.NewReader(strings.Repeat("w", 64)), "issuer-1")
	if err != nil {
		t.Fatal(err)
	}
	wrong := "AAAAAAAAAAAAAAAAAAAAAAAAAA" // wrong but shape-valid (26 chars)

	for i := 0; i < 4; i++ {
		if _, err := store.VerifyPairingCode("peer-a", wrong); err == nil {
			t.Fatalf("attempt %d: expected refusal for a wrong code", i)
		}
	}
	// 5th failure locks the peer out AND burns the real code.
	if _, err := store.VerifyPairingCode("peer-a", wrong); err == nil {
		t.Fatal("expected the 5th attempt to be refused")
	}
	if _, err := store.VerifyPairingCode("peer-a", code); err == nil {
		t.Fatal("expected the locked-out peer to be refused even with the REAL code")
	}
}

func TestPairingCode_UnknownRefused(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Now())
	store := NewPairingStore(clock)
	if _, err := store.VerifyPairingCode("peer-a", "ZZZZZZZZZZZZZZZZZZZZZZZZZZ"); err == nil {
		t.Fatal("expected an unknown code to be refused")
	}
}

func TestPairingCode_EmptyArgsRefused(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Now())
	store := NewPairingStore(clock)
	if _, err := store.VerifyPairingCode("", "AAAAAAAAAAAAAAAAAAAAAAAAAA"); err == nil {
		t.Fatal("expected empty peerID to be refused")
	}
	if _, err := store.VerifyPairingCode("peer-a", ""); err == nil {
		t.Fatal("expected empty code to be refused")
	}
}

func TestGeneratePairingCode_RequiresIssuingNodeID(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Now())
	store := NewPairingStore(clock)
	if _, err := store.GeneratePairingCode(strings.NewReader(strings.Repeat("a", 64)), ""); err == nil {
		t.Fatal("expected a refusal for an empty issuing node id")
	}
}

// TestPairingPeerLockoutExpiresAfterWindow proves a locked-out peer's
// rolling-hour window expiring resets it to not-locked-out, rather than
// locking a peer out forever.
func TestPairingPeerLockoutExpiresAfterWindow(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store := NewPairingStore(clock)
	wrong := "AAAAAAAAAAAAAAAAAAAAAAAAAA"
	for i := 0; i < 5; i++ {
		_, _ = store.VerifyPairingCode("peer-expiry", wrong)
	}
	if !store.peerLockedOut("peer-expiry", clock.Now()) {
		t.Fatal("expected the peer to be locked out after 5 failures")
	}
	clock.Advance(time.Hour + time.Second)
	if store.peerLockedOut("peer-expiry", clock.Now()) {
		t.Fatal("expected the lockout to expire once its rolling hour has passed")
	}
}

// TestGeneratePairingCode_EntropyReadErrorPropagates proves a broken
// entropy source refuses rather than issuing a low-entropy code.
func TestGeneratePairingCode_EntropyReadErrorPropagates(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Now())
	store := NewPairingStore(clock)
	if _, err := store.GeneratePairingCode(errReader{}, "issuer-1"); err == nil {
		t.Fatal("expected the entropy read error to propagate")
	}
}

func TestDecodePairingCode_MalformedRefused(t *testing.T) {
	cases := []string{"", "short", strings.Repeat("A", 25), strings.Repeat("A", 27), strings.Repeat("!", 26), strings.Repeat("A", 25) + "="}
	for _, c := range cases {
		_, decErr := DecodePairingCode(c)
		if decErr == nil {
			t.Fatalf("expected refusal for %q", c)
		}
		if k, ok := cascade.KindOf(decErr); !ok || k != cascade.KindInvalidInput {
			t.Fatalf("expected KindInvalidInput for %q, got %v (ok=%v)", c, k, ok)
		}
	}
}
