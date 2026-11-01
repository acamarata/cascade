package attest

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestAttestorClass(t *testing.T) {
	cases := map[string]AttestorClass{
		"enrolled-node":  ClassEnrolledNode,
		"ephemeral-oidc": ClassEphemeralOIDC,
		"unattested":     ClassUnattested,
		"unverified":     ClassUnverified,
		"":               ClassUnverified,
		"Enrolled-Node":  ClassUnverified,
		"root":           ClassUnverified,
	}
	for in, want := range cases {
		if got := ParseAttestorClass(in); got != want {
			t.Errorf("ParseAttestorClass(%q) = %q, want %q", in, got, want)
		}
	}
	eligible := map[AttestorClass]bool{
		ClassEnrolledNode: true, ClassEphemeralOIDC: true, ClassUnattested: false, ClassUnverified: false,
		AttestorClass(""): false, AttestorClass("other"): false,
	}
	for c, want := range eligible {
		if c.Eligible() != want {
			t.Errorf("%q.Eligible() = %v, want %v", c, !want, want)
		}
	}
	var zero AttestorClass
	if zero.Eligible() || ParseAttestorClass(string(zero)) != ClassUnverified {
		t.Error("the zero AttestorClass is permissive")
	}
}

// countersignFunc adapts a function to CountersignVerifier.
type countersignFunc func(SignedAttestation) error

func (f countersignFunc) VerifyCountersign(_ context.Context, sa SignedAttestation) error {
	return f(sa)
}

func TestAttestorClassDerivedNotAsserted(t *testing.T) {
	n := NewTestNode(t)
	accept := countersignFunc(func(SignedAttestation) error { return nil })
	refuse := countersignFunc(func(SignedAttestation) error { return errors.New("no countersignature") })

	if got := DeriveClass(n.MustAttest(t, n.SampleRecord()), n.Trust(), nil); got != ClassEnrolledNode {
		t.Fatalf("DeriveClass(enrolled node record) = %q, want enrolled-node", got)
	}
	asserted := n.SampleRecord()
	asserted.AttestorClass = ClassEphemeralOIDC
	if got := DeriveClass(n.MustAttest(t, asserted), n.Trust(), accept); got != ClassUnverified {
		t.Errorf("node-signed record asserting ephemeral-oidc derived %q, want unverified", got)
	}
	stranger := NewTestNode(t)
	if got := DeriveClass(stranger.MustAttest(t, stranger.SampleRecord()), n.Trust(), accept); got != ClassUnverified {
		t.Errorf("enrolled-node claim with a key in no store derived %q, want unverified", got)
	}
	if got := DeriveClass(n.MustAttest(t, n.SampleRecord()), nil, accept); got != ClassUnverified {
		t.Errorf("no trust store derived %q, want unverified", got)
	}
	unasserted := n.SampleRecord()
	unasserted.AttestorClass = ""
	if got := DeriveClass(n.MustAttest(t, unasserted), n.Trust(), nil); got != ClassUnverified {
		t.Errorf("record asserting no class derived %q, want unverified", got)
	}

	assertOIDCDerivation(t, n, accept, refuse)
}

// assertOIDCDerivation covers the ephemeral-oidc branch of DeriveClass.
func assertOIDCDerivation(t *testing.T, n *TestNode, accept, refuse CountersignVerifier) {
	t.Helper()
	oidc := n.SampleRecord()
	oidc.AttestorClass = ClassEphemeralOIDC
	oidc.OIDCTokenSHA256, oidc.OIDCRunID = strings.Repeat("9a", 32), "run-42"
	sa := n.MustAttest(t, oidc)
	if got := DeriveClass(sa, n.Trust(), accept); got != ClassEphemeralOIDC {
		t.Errorf("countersigned oidc record derived %q, want ephemeral-oidc", got)
	}
	for name, cs := range map[string]CountersignVerifier{"refused": refuse, "absent": nil} {
		if got := DeriveClass(sa, n.Trust(), cs); got != ClassUnverified {
			t.Errorf("oidc record with countersign %s derived %q, want unverified", name, got)
		}
	}
	half := oidc
	half.OIDCRunID = ""
	if got := DeriveClass(n.MustAttest(t, half), n.Trust(), accept); got != ClassUnverified {
		t.Errorf("record with one oidc field derived %q, want unverified", got)
	}
	enrolledWithOIDC := oidc
	enrolledWithOIDC.AttestorClass = ClassEnrolledNode
	if got := DeriveClass(n.MustAttest(t, enrolledWithOIDC), n.Trust(), accept); got != ClassUnverified {
		t.Errorf("enrolled-node claim carrying oidc fields derived %q, want unverified", got)
	}
	forged := n.MustAttest(t, n.SampleRecord())
	forged.Record.AttestorClass = ClassEphemeralOIDC
	forged.CanonicalJSON = forged.CanonicalJSON[:len(forged.CanonicalJSON)-1]
	if got := DeriveClass(forged, n.Trust(), accept); got != ClassUnverified {
		t.Errorf("record with broken canonical bytes derived %q, want unverified", got)
	}
}
