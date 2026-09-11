// Purpose: the zstd compress stage and the age encrypt/decrypt stage —
//
//	compress-THEN-encrypt (00-VISION principle 10: the order is decided,
//	not a choice, since compressing already-random-looking ciphertext is
//	a no-op that only adds a length-pattern leak) — via pure-Go libraries
//	(klauspost/compress/zstd: no CGO per 06 §2; filippo.io/age: the
//	REFERENCE implementation, never a hand-rolled cipher per Art.2).
//
// Inputs: raw chunk bytes (Compress), a compressed payload plus an age
//
//	recipient string (Encrypt), or ciphertext plus an age identity string
//	(Decrypt).
//
// Outputs: compressed bytes, ciphertext, or recovered plaintext.
// Constraints: Encrypt fails closed on an empty recipient — this package
//
//	never writes an unencrypted chunk. Decrypt returns NO plaintext bytes
//	when age's own STREAM authentication fails (a corrupted, truncated, or
//	tampered ciphertext), only a KindIntegrity error.
//
// SPORT: internal.backup.crypto/ADDED (P1-E19-W4-S41-T1).

package backup

import (
	"bytes"
	"io"

	"filippo.io/age"
	"github.com/klauspost/compress/zstd"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Compress zstd-compresses payload. Must run BEFORE Encrypt (compress-then-
// encrypt): once age has encrypted a payload the result is
// indistinguishable from random bytes and zstd can no longer find any
// redundancy to remove. Compressing before encryption instead leaks the
// PLAINTEXT's approximate length through the ciphertext's length — a
// tradeoff 00-VISION principle 10 accepts explicitly rather than pay
// zstd's cost for nothing.
func Compress(payload []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "backup: open zstd writer")
	}
	if _, err := w.Write(payload); err != nil {
		_ = w.Close()
		return nil, cascade.Wrap(cascade.KindInternal, err, "backup: zstd compress")
	}
	if err := w.Close(); err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "backup: close zstd writer")
	}
	return buf.Bytes(), nil
}

// Decompress reverses Compress. A truncated or non-zstd input refuses with
// a KindIntegrity error rather than returning partial bytes.
func Decompress(payload []byte) ([]byte, error) {
	r, err := zstd.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "backup: open zstd reader")
	}
	defer r.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "backup: zstd decompress")
	}
	return out, nil
}

// Encrypt age-encrypts compressed to recipientStr, the repo's public age
// recipient (config/'s only key material — 00-VISION principle 10:
// "recovery keys never live beside backups"). The REFERENCE age library
// draws a fresh random file key and STREAM nonce from crypto/rand on every
// call, so two calls over byte-identical input never produce identical
// ciphertext — the property that makes ciphertext-level dedup impossible
// and is exactly why Dedup hashes the pre-encryption payload instead
// (dedup.go's ObjectHash). An empty recipientStr fails closed: this
// function never writes an unencrypted chunk.
func Encrypt(recipientStr string, compressed []byte) ([]byte, error) {
	if recipientStr == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "backup: refusing to encrypt without an age recipient")
	}
	recipient, err := age.ParseX25519Recipient(recipientStr)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "backup: parse age recipient")
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, recipient)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "backup: open age writer")
	}
	if _, err := w.Write(compressed); err != nil {
		_ = w.Close()
		return nil, cascade.Wrap(cascade.KindInternal, err, "backup: age encrypt")
	}
	if err := w.Close(); err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "backup: close age writer")
	}
	return buf.Bytes(), nil
}

// Decrypt reverses Encrypt using identityStr, the corresponding age
// private identity (S-42.T6's recovery-key ceremony owns issuing and
// custodying it; this function only ever holds the string for the
// duration of one call and never persists or logs it). A corrupted,
// truncated, or tampered ciphertext fails the STREAM construction's
// authentication tag: age's Decrypt/Reader surfaces that as an error
// before any unauthenticated byte reaches the caller, so this function
// discards whatever io.ReadAll buffered and returns nil, err rather than a
// partial plaintext. An empty identityStr fails closed with the same
// KindInvalidInput Encrypt uses for a missing recipient.
func Decrypt(identityStr string, ciphertext []byte) ([]byte, error) {
	if identityStr == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "backup: refusing to decrypt without an age identity")
	}
	identity, err := age.ParseX25519Identity(identityStr)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "backup: parse age identity")
	}
	r, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "backup: age decrypt")
	}
	out, err := io.ReadAll(r)
	if err != nil {
		// A partially-buffered `out` may exist here, but authentication
		// did not pass, so it is discarded rather than returned.
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "backup: age decrypt payload")
	}
	return out, nil
}
