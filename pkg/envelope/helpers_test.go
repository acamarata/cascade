package envelope

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// testKind is an extension kind registered for tests: Data is {"max":N}
// and a child narrows when its max is at most the parent's.
const testKind = "test.budget"

func init() {
	RegisterExtension(testKind, func(parent, child json.RawMessage) error {
		var p, c struct{ Max int }
		if err := json.Unmarshal(parent, &p); err != nil {
			return err
		}
		if err := json.Unmarshal(child, &c); err != nil {
			return err
		}
		if c.Max > p.Max {
			return errors.New("max exceeds the parent's")
		}
		return nil
	})
}

// t0 is the fixed issue time every fixture uses.
var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// memSigner is an in-memory Signer whose seed lives in a t.TempDir-scoped
// test keystore; it never touches the platform keychain.
type memSigner struct{ priv ed25519.PrivateKey }

func (m memSigner) PublicKey() ed25519.PublicKey { return m.priv.Public().(ed25519.PublicKey) }

func (m memSigner) Sign(_ context.Context, msg []byte) ([]byte, error) {
	return ed25519.Sign(m.priv, msg), nil
}

// testKeystore is the test seam for key material: one seed file per named
// identity under a t.TempDir directory.
type testKeystore struct{ dir string }

func newTestKeystore(t *testing.T) testKeystore {
	t.Helper()
	return testKeystore{dir: t.TempDir()}
}

// signer returns the identity named name, creating its seed on first use.
func (k testKeystore) signer(t *testing.T, name string) memSigner {
	t.Helper()
	p := filepath.Join(k.dir, name+".seed")
	seed, err := os.ReadFile(p)
	if err != nil {
		seed = make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, seed, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return memSigner{priv: ed25519.NewKeyFromSeed(seed)}
}

// rootEnvelope returns a normalised root envelope issued by s.
func rootEnvelope(s memSigner) Envelope {
	return Envelope{
		Version:     Version,
		ID:          "env-root",
		Issuer:      fingerprint(s.PublicKey()),
		Principal:   "principal-1",
		Roots:       []string{"/a/b", "/srv/work"},
		Tools:       []string{"edit", "read"},
		Commands:    []string{"git", "go"},
		Egress:      []string{"model", "vcs"},
		Grants:      []GrantRef{{ID: "g1", Verb: "read", ExpiresAt: t0.Add(time.Hour)}},
		Budget:      Budget{WallClock: time.Hour, Tokens: 1000, CostMicros: 500, Procs: 4},
		Ext:         []Extension{{Kind: testKind, Data: json.RawMessage(`{"max":10}`)}},
		Fence:       Fence{DispatchID: "d1", Attempt: 1, LeaseEpoch: 7},
		Sensitivity: provider.SensitivityRestricted,
		IssuedAt:    t0,
		ExpiresAt:   t0.Add(2 * time.Hour),
	}
}

// childEnvelope returns an exact copy of p that names p as its parent.
func childEnvelope(t *testing.T, p Envelope) Envelope {
	t.Helper()
	c := p
	c.ID = "env-child"
	c.Roots = append([]string(nil), p.Roots...)
	c.Tools = append([]string(nil), p.Tools...)
	c.Commands = append([]string(nil), p.Commands...)
	c.Egress = append([]string(nil), p.Egress...)
	c.Grants = append([]GrantRef(nil), p.Grants...)
	c.Ext = append([]Extension(nil), p.Ext...)
	d, err := Digest(p)
	if err != nil {
		t.Fatal(err)
	}
	c.ParentDigest = d
	return c
}

// wantKind fails unless err carries kind and a message containing msg.
func wantKind(t *testing.T, err error, kind cascade.Kind, msg string) {
	t.Helper()
	if err == nil {
		t.Fatalf("got nil error, want %v containing %q", kind, msg)
	}
	if k, ok := cascade.KindOf(err); !ok || k != kind {
		t.Fatalf("error %q has kind %v (ok=%v), want %v", err, k, ok, kind)
	}
	if !strings.Contains(err.Error(), msg) {
		t.Fatalf("error %q does not contain %q", err, msg)
	}
}

// mustJSON marshals v or fails.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
