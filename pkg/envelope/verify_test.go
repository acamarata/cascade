package envelope

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestVerifyRefuses covers every refusal Verify owes, Kind and message.
func TestVerifyRefuses(t *testing.T) {
	ks := newTestKeystore(t)
	ctl := ks.signer(t, "controller")
	node := ks.signer(t, "node-identity")   // an enrolled node identity key
	stray := ks.signer(t, "unpinned-stray") // any other ed25519 key
	pinned := ctl.PublicKey()
	good, err := Issue(context.Background(), ctl, rootEnvelope(ctl))
	if err != nil {
		t.Fatal(err)
	}
	now := t0.Add(time.Minute)
	if env, err := Verify(good, pinned, now); err != nil || !reflect.DeepEqual(env, good.Env) {
		t.Fatalf("Verify(valid) = %v, %v", env, err)
	}
	cases := []struct {
		name   string
		s      Signed
		pinned ed25519.PublicKey
		now    time.Time
		kind   cascade.Kind
		msg    string
	}{
		{"unsigned", Signed{Env: good.Env}, pinned, now, cascade.KindIntegrity, "unsigned envelope"},
		{"node key as issuer", issueAs(t, node, rootEnvelope(node)), pinned, now, cascade.KindIntegrity, "is not the pinned controller"},
		{"node key claiming the controller", forge(t, node, good.Env, domainSeparator), pinned, now, cascade.KindIntegrity, "does not verify"},
		{"unpinned key as issuer", issueAs(t, stray, rootEnvelope(stray)), pinned, now, cascade.KindIntegrity, "is not the pinned controller"},
		{"unpinned key claiming the controller", forge(t, stray, good.Env, domainSeparator), pinned, now, cascade.KindIntegrity, "does not verify"},
		{"other domain separator", forge(t, ctl, good.Env, "cascade.scope-envelope.v0\n"), pinned, now, cascade.KindIntegrity, "does not verify"},
		{"sig length 63", Signed{Env: good.Env, Sig: good.Sig[:63]}, pinned, now, cascade.KindIntegrity, "signature has length 63"},
		{"sig length 65", Signed{Env: good.Env, Sig: append(append([]byte(nil), good.Sig...), 0)}, pinned, now, cascade.KindIntegrity, "signature has length 65"},
		{"expired", good, pinned, good.Env.ExpiresAt, cascade.KindPermissionDenied, "expired"},
		{"not yet valid", good, pinned, t0.Add(-time.Nanosecond), cascade.KindPermissionDenied, "not yet valid"},
		{"nil pinned key", good, nil, now, cascade.KindIntegrity, "pinned controller key has length 0"},
		{"empty pinned key", good, ed25519.PublicKey{}, now, cascade.KindIntegrity, "pinned controller key has length 0"},
		{"31-byte pinned key", good, pinned[:31], now, cascade.KindIntegrity, "pinned controller key has length 31"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := Verify(tc.s, tc.pinned, tc.now)
			wantKind(t, err, tc.kind, tc.msg)
			if !reflect.DeepEqual(env, Envelope{}) {
				t.Fatalf("Verify returned an envelope alongside %v", err)
			}
		})
	}
	verifyTampered(t, good, pinned, now)
}

// verifyTampered changes one field of a validly signed envelope per case
// and expects KindIntegrity.
func verifyTampered(t *testing.T, good Signed, pinned ed25519.PublicKey, now time.Time) {
	for name, tamper := range tampers() {
		t.Run("tampered "+name, func(t *testing.T) {
			s := Signed{Env: childEnvelope(t, good.Env), Sig: good.Sig}
			s.Env.ParentDigest, s.Env.ID = good.Env.ParentDigest, good.Env.ID
			tamper(&s.Env)
			_, err := Verify(s, pinned, now)
			want := "signature does not verify"
			if name == "issuer" {
				want = "is not the pinned controller"
			}
			wantKind(t, err, cascade.KindIntegrity, want)
		})
	}
}

// tampers changes one byte (or one unit) of one field each.
func tampers() map[string]func(e *Envelope) {
	return map[string]func(e *Envelope){
		"id":          func(e *Envelope) { e.ID = "env-roou" },
		"parent":      func(e *Envelope) { e.ParentDigest = "x" },
		"issuer":      func(e *Envelope) { e.Issuer = e.Issuer[:len(e.Issuer)-1] + flipHex(e.Issuer[len(e.Issuer)-1:]) },
		"principal":   func(e *Envelope) { e.Principal = "principal-2" },
		"root":        func(e *Envelope) { e.Roots[0] = "/a/c" },
		"tool":        func(e *Envelope) { e.Tools[0] = "edis" },
		"cmd":         func(e *Envelope) { e.Commands[1] = "gp" },
		"egress":      func(e *Envelope) { e.Egress[1] = "vct" },
		"grant verb":  func(e *Envelope) { e.Grants[0].Verb = "reae" },
		"grant exp":   func(e *Envelope) { e.Grants[0].ExpiresAt = e.Grants[0].ExpiresAt.Add(time.Nanosecond) },
		"budget":      func(e *Envelope) { e.Budget.Tokens++ },
		"ext data":    func(e *Envelope) { e.Ext[0].Data = json.RawMessage(`{"max":11}`) },
		"fence":       func(e *Envelope) { e.Fence.LeaseEpoch++ },
		"sensitivity": func(e *Envelope) { e.Sensitivity = provider.SensitivityInternal },
		"issued at":   func(e *Envelope) { e.IssuedAt = e.IssuedAt.Add(-time.Nanosecond) },
		"expires at":  func(e *Envelope) { e.ExpiresAt = e.ExpiresAt.Add(time.Nanosecond) },
	}
}

// flipHex returns a hex digit different from d.
func flipHex(d string) string {
	if d == "0" {
		return "1"
	}
	return "0"
}

// issueAs issues e with s or fails.
func issueAs(t *testing.T, s memSigner, e Envelope) Signed {
	t.Helper()
	out, err := Issue(context.Background(), s, e)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// forge signs e's canonical body behind sep with s, keeping e's Issuer.
func forge(t *testing.T, s memSigner, e Envelope, sep string) Signed {
	t.Helper()
	c, err := Canonical(e)
	if err != nil {
		t.Fatal(err)
	}
	msg := append([]byte(sep), c[len(domainSeparator):]...)
	sig, _ := s.Sign(context.Background(), msg)
	return Signed{Env: e, Sig: sig}
}

// TestVerifyAcceptsShiftedZone proves a time zone shift that keeps the
// instant keeps the signature valid, and Canonical is zone-independent.
func TestVerifyAcceptsShiftedZone(t *testing.T) {
	ks := newTestKeystore(t)
	ctl := ks.signer(t, "controller")
	good := issueAs(t, ctl, rootEnvelope(ctl))
	shifted := good
	shifted.Env.ExpiresAt = good.Env.ExpiresAt.In(time.FixedZone("x", 5*3600))
	if _, err := Verify(shifted, ctl.PublicKey(), t0); err != nil {
		t.Fatalf("Verify(zone-shifted same instant) = %v", err)
	}
}

// failSigner is a Signer whose Sign fails or returns a bad signature.
type failSigner struct {
	memSigner
	err error
	sig []byte
}

func (f failSigner) Sign(context.Context, []byte) ([]byte, error) { return f.sig, f.err }

// shortKeySigner exposes a 31-byte public key.
type shortKeySigner struct{}

func (shortKeySigner) PublicKey() ed25519.PublicKey                 { return make([]byte, 31) }
func (shortKeySigner) Sign(context.Context, []byte) ([]byte, error) { return nil, nil }

// TestIssueRefuses covers Issue's own refusals.
func TestIssueRefuses(t *testing.T) {
	ks := newTestKeystore(t)
	ctl := ks.signer(t, "controller")
	e := rootEnvelope(ctl)
	_, err := Issue(context.Background(), nil, e)
	wantKind(t, err, cascade.KindInvalidInput, "nil signer")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Issue(ctx, ctl, e)
	wantKind(t, err, cascade.KindCanceled, "issue")
	_, err = Issue(context.Background(), ks.signer(t, "other"), e)
	wantKind(t, err, cascade.KindInvalidInput, "is not the signer")
	_, err = Issue(context.Background(), failSigner{memSigner: ctl, err: errors.New("hsm offline")}, e)
	wantKind(t, err, cascade.KindInternal, "hsm offline")
	_, err = Issue(context.Background(), failSigner{memSigner: ctl, sig: make([]byte, 64)}, e)
	wantKind(t, err, cascade.KindIntegrity, "does not verify")
	_, err = Issue(context.Background(), shortKeySigner{}, e)
	wantKind(t, err, cascade.KindIntegrity, "wrong length")
	bad := e
	bad.Tools = []string{"z", "a"}
	_, err = Issue(context.Background(), ctl, bad)
	wantKind(t, err, cascade.KindInvalidInput, "tools")
	e.Issuer = ""
	if s := issueAs(t, ctl, e); s.Env.Issuer != fingerprint(ctl.PublicKey()) {
		t.Fatalf("Issue left Issuer %q", s.Env.Issuer)
	}
}
