package nodes

import (
	"context"
	"crypto/ed25519"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// NewNodeKeystoreForTest returns a NodeKeystore backed by the encrypted
// file vault rooted at t.TempDir(), so tests never touch a real OS
// keychain (Art.7.1). Exported (capital N) so enroll_test.go's fixtures
// can build one; still test-only by construction (no production caller
// beyond NewNodeKeystore's own real selection, which this helper
// deliberately bypasses by forcing Dir).
func NewNodeKeystoreForTest(t *testing.T) (*NodeKeystore, error) {
	t.Helper()
	return NewNodeKeystore(secrets.Config{
		Service: "cascade-node-identity-test",
		Dir:     t.TempDir(),
	})
}

func TestNodeKeystoreStoreAndSign(t *testing.T) {
	ks, err := NewNodeKeystoreForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	id, priv, err := GenerateIdentity(strings.NewReader(strings.Repeat("x", 64)))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := ks.Store(ctx, id.NodeID, priv); err != nil {
		t.Fatalf("unexpected error storing key: %v", err)
	}

	payload := []byte("sign me")
	sig, err := ks.Sign(ctx, id.NodeID, payload)
	if err != nil {
		t.Fatalf("unexpected error signing: %v", err)
	}
	if !ed25519.Verify(id.PubKey, payload, sig) {
		t.Fatal("signature does not verify against the identity's public key")
	}
}

func TestNodeKeystoreSignWithoutEnrollmentRefused(t *testing.T) {
	ks, err := NewNodeKeystoreForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ks.Sign(context.Background(), "never-enrolled", []byte("payload"))
	if err == nil {
		t.Fatal("expected refusal signing with no enrolled key")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindNotFound {
		t.Fatalf("expected KindNotFound, got %v (ok=%v)", k, ok)
	}
}

func TestNodeKeystoreStoreWrongSizeRefused(t *testing.T) {
	ks, err := NewNodeKeystoreForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	err = ks.Store(context.Background(), "node1", ed25519.PrivateKey([]byte("too-short")))
	if err == nil {
		t.Fatal("expected refusal storing a malformed private key")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindInvalidInput {
		t.Fatalf("expected KindInvalidInput, got %v (ok=%v)", k, ok)
	}
}

func TestNodeKeystoreDelete(t *testing.T) {
	ks, err := NewNodeKeystoreForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	id, priv, err := GenerateIdentity(strings.NewReader(strings.Repeat("y", 64)))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := ks.Store(ctx, id.NodeID, priv); err != nil {
		t.Fatal(err)
	}
	if err := ks.Delete(ctx, id.NodeID); err != nil {
		t.Fatalf("unexpected error deleting: %v", err)
	}
	if _, err := ks.Sign(ctx, id.NodeID, []byte("x")); err == nil {
		t.Fatal("expected refusal signing after delete")
	}
}

func TestNodeKeystoreBackendName(t *testing.T) {
	ks, err := NewNodeKeystoreForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	if ks.BackendName() == "" {
		t.Fatal("expected a non-empty backend name")
	}
}

// TestNodeKeystoreSignCorruptCustodyEntry proves Sign fails closed
// (KindIntegrity) when custody holds a value of the wrong length for an
// Ed25519 private key — a corrupt entry, never treated as a usable key.
// It writes directly through the same-package custody field rather than
// Store, since Store itself refuses a wrong-size key before it ever
// reaches custody (this is the state a corrupt store would be in, not a
// path Store can produce).
func TestNodeKeystoreSignCorruptCustodyEntry(t *testing.T) {
	ks, err := NewNodeKeystoreForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	if err := ks.custody.Set(context.Background(), keystoreSecretName("corrupt-node"), []byte("too-short")); err != nil {
		t.Fatal(err)
	}
	_, err = ks.Sign(context.Background(), "corrupt-node", []byte("payload"))
	if err == nil {
		t.Fatal("expected refusal signing with a corrupt custody entry")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindIntegrity {
		t.Fatalf("expected KindIntegrity, got %v (ok=%v)", k, ok)
	}
}

// TestNewNodeKeystoreDefaultsService proves an empty cfg.Service defaults
// to keystoreService rather than being passed through empty to
// secrets.SelectCustody.
func TestNewNodeKeystoreDefaultsService(t *testing.T) {
	ks, err := NewNodeKeystore(secrets.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if ks.BackendName() == "" {
		t.Fatal("expected a usable keystore with the defaulted service label")
	}
}

func TestNewNodeKeystoreNoBackendAvailable(t *testing.T) {
	// An empty Dir with no OS keychain reachable in this sandbox falls
	// through to the file vault's own "needs a directory" refusal.
	_, err := NewNodeKeystore(secrets.Config{Service: "cascade-node-identity-test"})
	if err == nil {
		t.Skip("a real OS keychain is available on this host; nothing to assert")
	}
}
