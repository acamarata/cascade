package nodes

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestKeyIDDerivation(t *testing.T) {
	seedA := sha256.Sum256([]byte("cascade public test seed A"))
	seedB := sha256.Sum256([]byte("cascade public test seed B"))
	pubA := ed25519.NewKeyFromSeed(seedA[:]).Public().(ed25519.PublicKey)
	pubB := ed25519.NewKeyFromSeed(seedB[:]).Public().(ed25519.PublicKey)
	sum := sha256.Sum256(pubA)
	if got, want := KeyIDFor(pubA), hex.EncodeToString(sum[:])[:16]; got != want {
		t.Fatalf("KeyIDFor = %q, want the first 16 hex chars of sha256(pub) %q", got, want)
	}
	if len(KeyIDFor(pubA)) != 16 || KeyIDFor(pubA) != KeyIDFor(append(ed25519.PublicKey(nil), pubA...)) {
		t.Fatal("KeyIDFor is not a stable 16-char id")
	}
	if KeyIDFor(pubA) == KeyIDFor(pubB) {
		t.Fatal("two keys share a key id")
	}
}
