// Purpose: Compress/Decompress and Encrypt/Decrypt round-trips, the
//
//	nonce-uniqueness proof (two encryptions of the same plaintext never
//	collide), and every integrity refusal path (corrupt tag, truncated
//	ciphertext, missing key material).
//
// SPORT: internal.backup.crypto/ADDED (P1-E19-W4-S41-T1).
package backup

import (
	"bytes"
	"testing"

	"filippo.io/age"

	"github.com/acamarata/cascade/pkg/cascade"
)

// newTestAgeKeypair generates a fresh real X25519 age identity/recipient
// pair via the reference library — shared by every test file in this
// package that needs key material.
func newTestAgeKeypair(t testing.TB) (identity, recipient string) {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}
	return id.String(), id.Recipient().String()
}

func TestCompressDecompressRoundTrip(t *testing.T) {
	cases := [][]byte{
		nil,
		[]byte(""),
		[]byte("a"),
		bytes.Repeat([]byte("cascade-backup-pipeline "), 4096),
	}
	for _, want := range cases {
		compressed, err := Compress(want)
		if err != nil {
			t.Fatalf("Compress(%d bytes): %v", len(want), err)
		}
		got, err := Decompress(compressed)
		if err != nil {
			t.Fatalf("Decompress: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("round trip mismatch: got %d bytes, want %d bytes", len(got), len(want))
		}
	}
}

func TestDecompressRejectsGarbage(t *testing.T) {
	if _, err := Decompress([]byte("not zstd data")); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Decompress(garbage) error kind = %v, want KindIntegrity", err)
	}
}

func TestEncryptAgeRoundTrip(t *testing.T) {
	identity, recipient := newTestAgeKeypair(t)
	plaintext := []byte("cascade backup chunk payload")

	ciphertext, err := Encrypt(recipient, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	got, err := Decrypt(identity, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round trip mismatch: got %q, want %q", got, plaintext)
	}
}

// TestEncryptNeverReusesCiphertext proves the nonce-uniqueness property the
// whole dedup design depends on: two Encrypt calls over byte-identical
// plaintext never produce the same ciphertext, because the reference age
// library draws a fresh random file key/nonce every call. This is exactly
// why Dedup hashes the PRE-encryption payload rather than the stored
// bytes.
func TestEncryptNeverReusesCiphertext(t *testing.T) {
	_, recipient := newTestAgeKeypair(t)
	plaintext := []byte("identical plaintext chunk")

	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		ciphertext, err := Encrypt(recipient, plaintext)
		if err != nil {
			t.Fatalf("Encrypt iteration %d: %v", i, err)
		}
		key := string(ciphertext)
		if seen[key] {
			t.Fatalf("Encrypt produced a repeated ciphertext at iteration %d: nonce reuse", i)
		}
		seen[key] = true
	}
}

func TestEncryptRefusesEmptyRecipient(t *testing.T) {
	_, err := Encrypt("", []byte("data"))
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Encrypt(empty recipient) error kind = %v, want KindInvalidInput", err)
	}
}

func TestDecryptRefusesEmptyIdentity(t *testing.T) {
	_, err := Decrypt("", []byte("data"))
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Decrypt(empty identity) error kind = %v, want KindInvalidInput", err)
	}
}

// TestDecryptRefusesTamperedCiphertext proves a corrupted, truncated or
// tampered chunk is REFUSED on read, never silently returned: flipping one
// byte anywhere in a real ciphertext must fail the STREAM construction's
// authentication tag, and no plaintext bytes may reach the caller.
func TestDecryptRefusesTamperedCiphertext(t *testing.T) {
	identity, recipient := newTestAgeKeypair(t)
	ciphertext, err := Encrypt(recipient, []byte("do not tamper with this chunk"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	tampered := append([]byte(nil), ciphertext...)
	tampered[len(tampered)-1] ^= 0xFF // flip the last byte, inside the STREAM's final authenticated block

	got, err := Decrypt(identity, tampered)
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Decrypt(tampered) error kind = %v, want KindIntegrity", err)
	}
	if got != nil {
		t.Fatalf("Decrypt(tampered) returned %d bytes of unauthenticated plaintext, want nil", len(got))
	}
}

func TestDecryptRefusesTruncatedCiphertext(t *testing.T) {
	identity, recipient := newTestAgeKeypair(t)
	ciphertext, err := Encrypt(recipient, []byte("a payload long enough to truncate meaningfully"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	truncated := ciphertext[:len(ciphertext)-8]

	got, err := Decrypt(identity, truncated)
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Decrypt(truncated) error kind = %v, want KindIntegrity", err)
	}
	if got != nil {
		t.Fatalf("Decrypt(truncated) returned %d bytes of unauthenticated plaintext, want nil", len(got))
	}
}

func TestDecryptRefusesWrongIdentity(t *testing.T) {
	_, recipient := newTestAgeKeypair(t)
	wrongIdentity, _ := newTestAgeKeypair(t)
	ciphertext, err := Encrypt(recipient, []byte("payload"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := Decrypt(wrongIdentity, ciphertext); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Decrypt(wrong identity) error kind = %v, want KindIntegrity", err)
	}
}
