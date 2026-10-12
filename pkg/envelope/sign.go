// Purpose: issue an envelope under the controller installation key and
//
//	verify a signed envelope against the pinned controller public key.
//
// Inputs: a Signer (the controller key custody, provisioned elsewhere), an
//
//	Envelope; or a Signed, the pinned public key and the current time.
//
// Outputs: a Signed envelope, or the verified Envelope.
// Constraints: Verify checks the pinned key length FIRST and never panics;
//
//	unsigned, foreign-signed, issuer-mismatched and tampered envelopes are
//	KindIntegrity, an envelope outside its validity window is
//	KindPermissionDenied. This package never creates, stores or pins a key.
//
// SPORT: pkg/envelope (ADD) — P1-SEC-20 (contract:scope-envelope).

package envelope

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// issuerPrefix prefixes the hex key fingerprint in Envelope.Issuer.
const issuerPrefix = "ed25519:"

// Signer is the controller installation key custody: it exposes the public
// key and signs a message without revealing the private key.
type Signer interface {
	// PublicKey returns the ed25519 public key the signatures verify under.
	PublicKey() ed25519.PublicKey
	// Sign returns the ed25519 signature of msg.
	Sign(ctx context.Context, msg []byte) ([]byte, error)
}

// Issue signs e with s. An empty Issuer is set to the signer's
// fingerprint; a different non-empty Issuer is refused. Times are stored
// in UTC so the returned Signed re-encodes to its canonical form.
func Issue(ctx context.Context, s Signer, e Envelope) (Signed, error) {
	if s == nil {
		return Signed{}, cascade.New(cascade.KindInvalidInput, "envelope: nil signer")
	}
	if err := ctx.Err(); err != nil {
		return Signed{}, cascade.Wrap(cascade.KindCanceled, err, "envelope: issue")
	}
	pub := s.PublicKey()
	if len(pub) != ed25519.PublicKeySize {
		return Signed{}, cascade.New(cascade.KindIntegrity, "envelope: signer public key has the wrong length")
	}
	fp := fingerprint(pub)
	if e.Issuer == "" {
		e.Issuer = fp
	}
	if e.Issuer != fp {
		return Signed{}, invalidf("issuer %q is not the signer %q", clip(e.Issuer), fp)
	}
	e = utcTimes(e)
	msg, err := Canonical(e)
	if err != nil {
		return Signed{}, err
	}
	sig, err := s.Sign(ctx, msg)
	if err != nil {
		return Signed{}, cascade.Wrap(cascade.KindInternal, err, "envelope: sign")
	}
	if len(sig) != ed25519.SignatureSize || !ed25519.Verify(pub, msg, sig) {
		return Signed{}, cascade.New(cascade.KindIntegrity, "envelope: signer returned a signature that does not verify")
	}
	return Signed{Env: e, Sig: sig}, nil
}

// Verify returns s.Env when s carries a valid signature by pinned over
// Canonical(s.Env), its Issuer names pinned, and now lies in
// [IssuedAt, ExpiresAt).
func Verify(s Signed, pinned ed25519.PublicKey, now time.Time) (Envelope, error) {
	if len(pinned) != ed25519.PublicKeySize {
		return Envelope{}, integrityf("pinned controller key has length %d, want %d", len(pinned), ed25519.PublicKeySize)
	}
	if len(s.Sig) == 0 {
		return Envelope{}, integrityf("unsigned envelope")
	}
	if len(s.Sig) != ed25519.SignatureSize {
		return Envelope{}, integrityf("signature has length %d, want %d", len(s.Sig), ed25519.SignatureSize)
	}
	if want := fingerprint(pinned); s.Env.Issuer != want {
		return Envelope{}, integrityf("issuer %q is not the pinned controller %q", clip(s.Env.Issuer), want)
	}
	msg, err := Canonical(s.Env)
	if err != nil {
		return Envelope{}, cascade.Wrap(cascade.KindIntegrity, err, "envelope: signed envelope is not canonical")
	}
	if !ed25519.Verify(pinned, msg, s.Sig) {
		return Envelope{}, integrityf("signature does not verify under the pinned controller key")
	}
	if now.Before(s.Env.IssuedAt) {
		return Envelope{}, cascade.New(cascade.KindPermissionDenied, "envelope: not yet valid")
	}
	if !now.Before(s.Env.ExpiresAt) {
		return Envelope{}, cascade.New(cascade.KindPermissionDenied, "envelope: expired")
	}
	return s.Env, nil
}

// fingerprint returns the Issuer form of pub.
func fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return issuerPrefix + hex.EncodeToString(sum[:])
}

// integrityf builds a KindIntegrity error with the package prefix.
func integrityf(format string, args ...any) error {
	return cascade.Newf(cascade.KindIntegrity, "envelope: "+format, args...)
}
