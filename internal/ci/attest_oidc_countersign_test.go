package ci

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/ci/attest"
)

// TestCountersignOIDCProducesDerivedClass verifies that a valid token
// produces an ephemeral-oidc record via DeriveClass, a failing token is
// refused, and a bare assertion derives unverified.
func TestCountersignOIDCProducesDerivedClass(t *testing.T) {
	env := newTestOIDCEnv(t)
	rec := minimalRecord()
	signer := func(b []byte) ([]byte, error) {
		sig := make([]byte, 64)
		copy(sig, b)
		return sig, nil
	}
	raw := env.signToken(t, env.validClaims(), "", "")
	sa, err := CountersignOIDC(context.Background(), rec, raw, env.want, env.source, signer, env.now)
	if err != nil {
		t.Fatalf("countersign: %v", err)
	}
	if sa.Record.AttestorClass != attest.ClassEphemeralOIDC {
		t.Fatalf("record class %q, want ephemeral-oidc", sa.Record.AttestorClass)
	}
	if sa.Record.OIDCTokenSHA256 == "" || sa.Record.OIDCRunID != env.want.RunID {
		t.Fatal("oidc fields not stamped")
	}
	cv := oidcCountersignVerifier(func(_ context.Context, got attest.SignedAttestation) error {
		if string(got.CanonicalJSON) != string(sa.CanonicalJSON) {
			return errors.New("countersign mismatch")
		}
		return nil
	})
	derived := attest.DeriveClass(sa, nil, cv)
	if derived != attest.ClassEphemeralOIDC {
		t.Fatalf("derived %q, want ephemeral-oidc", derived)
	}
	// Failing token refuses countersignature.
	badRaw := env.signToken(t, map[string]any{"iss": "bad"}, "", "")
	if _, err := CountersignOIDC(context.Background(), rec, badRaw, env.want, env.source, signer, env.now); err == nil {
		t.Fatal("expected error for bad token")
	}
	// Bare assertion without countersignature derives unverified.
	bare := minimalRecord()
	bare.AttestorClass = attest.ClassEphemeralOIDC
	bareSa, err := attest.Attest(bare, signer)
	if err != nil {
		t.Fatalf("attest bare: %v", err)
	}
	if got := attest.DeriveClass(bareSa, nil, nil); got != attest.ClassUnverified {
		t.Fatalf("bare derived %q, want unverified", got)
	}
}

// oidcCountersignVerifier adapts a function to attest.CountersignVerifier.
type oidcCountersignVerifier func(context.Context, attest.SignedAttestation) error

// VerifyCountersign implements attest.CountersignVerifier.
func (f oidcCountersignVerifier) VerifyCountersign(ctx context.Context, sa attest.SignedAttestation) error {
	return f(ctx, sa)
}

// minimalRecord returns a valid AttestationRecord for testing.
func minimalRecord() attest.AttestationRecord {
	return attest.AttestationRecord{
		SchemaVersion: 1,
		PolicyVersion: "v1",
		CheckpointID:  "cp-1",
		Nonce:         "nonce-1",
		CommitSHA:     strings.Repeat("aa", 20),
		TreeHash:      strings.Repeat("bb", 20),
		NodeID:        "node-1",
		KeyID:         "key-1",
		ExpiresAt:     time.Now().Add(24 * time.Hour),
		StartedAt:     time.Now(),
		EndedAt:       time.Now(),
		AttestorClass: attest.ClassUnattested,
	}
}
