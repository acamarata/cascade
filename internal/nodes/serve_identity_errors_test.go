package nodes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// failingCustody is a secrets.Custody fake whose Set always fails, so
// EnsureLocalIdentity's keystore.Store error branch can be exercised
// without depending on any real custody backend's own failure modes.
type failingCustody struct{}

func (failingCustody) Name() string    { return "failing-custody-test-fake" }
func (failingCustody) Available() bool { return true }
func (failingCustody) Set(context.Context, string, []byte) error {
	return cascade.New(cascade.KindUnavailable, "simulated custody write failure")
}
func (failingCustody) Get(context.Context, string) ([]byte, error) {
	return nil, cascade.New(cascade.KindNotFound, "not stored")
}
func (failingCustody) Delete(context.Context, string) error {
	return cascade.New(cascade.KindNotFound, "not stored")
}
func (failingCustody) List(context.Context) ([]string, error) { return nil, nil }

// errSelfIdentityBackend is a SelfIdentityBackend fake that can be told
// to fail Load or Save on demand, so EnsureLocalIdentity's own error
// branches (distinct from memSelfIdentityBackend's always-succeeds
// shape) can be exercised.
type errSelfIdentityBackend struct {
	loadErr error
	saveErr error
}

func (b *errSelfIdentityBackend) Load() (SelfIdentity, bool, error) {
	if b.loadErr != nil {
		return SelfIdentity{}, false, b.loadErr
	}
	return SelfIdentity{}, false, nil
}
func (b *errSelfIdentityBackend) Save(SelfIdentity) error { return b.saveErr }

func TestEnsureLocalIdentity_BackendLoadErrorPropagates(t *testing.T) {
	ks, err := NewNodeKeystoreForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	backend := &errSelfIdentityBackend{loadErr: cascade.New(cascade.KindUnavailable, "simulated read failure")}
	if _, err := EnsureLocalIdentity(context.Background(), backend, ks, nil); err == nil {
		t.Fatal("expected the backend Load error to propagate")
	}
}

func TestEnsureLocalIdentity_NilReaderUsesCryptoRand(t *testing.T) {
	// A fresh backend with no existing identity and a nil rnd argument:
	// this is the only path that reaches the "rnd == nil -> rand.Reader"
	// fallback (a corrupt-existing-backend case fails before ever
	// checking rnd).
	ks, err := NewNodeKeystoreForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	backend := &memSelfIdentityBackend{}
	id, err := EnsureLocalIdentity(context.Background(), backend, ks, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id.NodeID == "" {
		t.Fatal("expected a generated node id")
	}
}

func TestEnsureLocalIdentity_GenerateIdentityErrorPropagates(t *testing.T) {
	ks, err := NewNodeKeystoreForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	backend := &memSelfIdentityBackend{}
	if _, err := EnsureLocalIdentity(context.Background(), backend, ks, errReader{}); err == nil {
		t.Fatal("expected GenerateIdentity's entropy error to propagate")
	}
}

func TestEnsureLocalIdentity_KeystoreStoreErrorPropagates(t *testing.T) {
	ks := &NodeKeystore{custody: failingCustody{}}
	backend := &memSelfIdentityBackend{}
	if _, err := EnsureLocalIdentity(context.Background(), backend, ks, strings.NewReader(strings.Repeat("q", 64))); err == nil {
		t.Fatal("expected keystore.Store's error to propagate")
	}
}

func TestEnsureLocalIdentity_BackendSaveErrorPropagates(t *testing.T) {
	ks, err := NewNodeKeystoreForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	backend := &errSelfIdentityBackend{saveErr: cascade.New(cascade.KindUnavailable, "simulated write failure")}
	if _, err := EnsureLocalIdentity(context.Background(), backend, ks, strings.NewReader(strings.Repeat("r", 64))); err == nil {
		t.Fatal("expected the backend Save error to propagate")
	}
}

// TestFileSelfIdentityBackendLoad_UnreadableNotNotExist: a directory at
// the expected file path makes os.ReadFile fail with something other
// than IsNotExist, which must propagate rather than being swallowed.
func TestFileSelfIdentityBackendLoad_UnreadableNotNotExist(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "nodes", "self_identity.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewFileSelfIdentityBackend(dir).Load(); err == nil {
		t.Fatal("expected a read error for an unreadable (directory) store path")
	}
}

// TestFileControllerBindingBackendLoad_UnreadableNotNotExist: same
// EISDIR-not-IsNotExist distinction for the controller binding backend.
func TestFileControllerBindingBackendLoad_UnreadableNotNotExist(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "nodes", "controller_binding.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewFileControllerBindingBackend(dir).Load(); err == nil {
		t.Fatal("expected a read error for an unreadable (directory) store path")
	}
}
