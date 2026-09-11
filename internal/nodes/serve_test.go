package nodes

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/testkit"
)

// memSelfIdentityBackend is an in-memory SelfIdentityBackend fake.
type memSelfIdentityBackend struct {
	id *SelfIdentity
}

func (m *memSelfIdentityBackend) Load() (SelfIdentity, bool, error) {
	if m.id == nil {
		return SelfIdentity{}, false, nil
	}
	return *m.id, true, nil
}

func (m *memSelfIdentityBackend) Save(id SelfIdentity) error {
	m.id = &id
	return nil
}

func TestRefuseOnGOOS(t *testing.T) {
	if err := RefuseOnGOOS("windows"); err == nil {
		t.Fatal("expected refusal for goos=windows")
	}
	if err := RefuseOnGOOS("darwin"); err != nil {
		t.Fatalf("unexpected refusal for goos=darwin: %v", err)
	}
	if err := RefuseOnGOOS("linux"); err != nil {
		t.Fatalf("unexpected refusal for goos=linux: %v", err)
	}
}

func TestEnsureLocalIdentityFirstRunGeneratesAndPersists(t *testing.T) {
	ks, err := NewNodeKeystoreForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	backend := &memSelfIdentityBackend{}
	id, err := EnsureLocalIdentity(context.Background(), backend, ks, strings.NewReader(strings.Repeat("s", 64)))
	if err != nil {
		t.Fatal(err)
	}
	if id.NodeID == "" {
		t.Fatal("expected a generated node id")
	}
	// The private key must be in custody, not returned or re-derivable
	// from the backend record.
	sig, err := ks.Sign(context.Background(), id.NodeID, []byte("payload"))
	if err != nil {
		t.Fatalf("keystore should have the generated key: %v", err)
	}
	if len(sig) == 0 {
		t.Fatal("expected a non-empty signature")
	}
}

func TestEnsureLocalIdentityIdempotent(t *testing.T) {
	ks, err := NewNodeKeystoreForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	backend := &memSelfIdentityBackend{}
	first, err := EnsureLocalIdentity(context.Background(), backend, ks, strings.NewReader(strings.Repeat("s", 64)))
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnsureLocalIdentity(context.Background(), backend, ks, strings.NewReader(strings.Repeat("t", 64)))
	if err != nil {
		t.Fatal(err)
	}
	if first.NodeID != second.NodeID {
		t.Fatalf("second call generated a new identity: %s != %s", first.NodeID, second.NodeID)
	}
}

func TestEnsureLocalIdentityCorruptBackend(t *testing.T) {
	ks, err := NewNodeKeystoreForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	backend := &memSelfIdentityBackend{id: &SelfIdentity{NodeID: "bogus", PubKeyB64: "not-valid"}}
	if _, err := EnsureLocalIdentity(context.Background(), backend, ks, nil); err == nil {
		t.Fatal("expected refusal for a corrupt persisted identity")
	}
}

func newServeDeps(t *testing.T) ServeDeps {
	t.Helper()
	clock := testkit.NewFrozenClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ks, err := NewNodeKeystoreForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	self := testIdentity(t, "self")
	_, selfPriv, err := GenerateIdentity(strings.NewReader(strings.Repeat("self", 32)))
	if err != nil {
		t.Fatal(err)
	}
	if err := ks.Store(context.Background(), self.NodeID, selfPriv); err != nil {
		t.Fatal(err)
	}
	return ServeDeps{
		Records:      NewRecordStore(newMemRecordBackend(), clock),
		KnownHosts:   NewKnownHosts(newMemKnownHostsBackend()),
		Keystore:     ks,
		Self:         self,
		Precondition: func() (bool, bool) { return true, true },
		Sequences:    NewSequenceStore(),
		Clock:        clock,
	}
}

// TestBuildServeRegistry_EndToEndThroughDispatch proves BuildServeRegistry
// is the real, load-bearing composition: node.heartbeat dispatches
// through the exact registry cmd/cascade/node_serve.go serves.
func TestBuildServeRegistry_EndToEndThroughDispatch(t *testing.T) {
	deps := newServeDeps(t)
	id := testIdentity(t, "peer")
	rec, err := deps.Records.Enroll(id, TierWorkerTrusted)
	if err != nil {
		t.Fatal(err)
	}
	_, priv, err := GenerateIdentity(strings.NewReader(strings.Repeat("peer", 32)))
	if err != nil {
		t.Fatal(err)
	}
	registry := BuildServeRegistry(deps)
	f := signedFrame(t, priv, id.NodeID, DeriveEnrollmentID(rec), 1)
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	_, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: "node.heartbeat", Params: raw})
	if errObj != nil {
		t.Fatalf("dispatch failed: %+v", errObj)
	}
}

// TestBuildServeRegistry_WithoutCall_MethodNotFound proves the wiring
// above is load-bearing: a bare rpc.NewRegistry() that never goes through
// BuildServeRegistry has neither method mounted.
func TestBuildServeRegistry_WithoutCall_MethodNotFound(t *testing.T) {
	registry := rpc.NewRegistry() // BuildServeRegistry deliberately NOT called
	_, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: "node.heartbeat", Params: []byte(`{}`)})
	if errObj == nil {
		t.Fatal("expected method-not-found")
	}
	if errObj.Code != -32601 {
		t.Fatalf("errObj.Code = %d, want -32601", errObj.Code)
	}
}

func TestFileSelfIdentityBackendRoundTrip(t *testing.T) {
	dir := t.TempDir()
	backend := NewFileSelfIdentityBackend(dir)
	if _, ok, err := backend.Load(); err != nil || ok {
		t.Fatalf("expected no identity yet, got ok=%v err=%v", ok, err)
	}
	want := SelfIdentity{NodeID: "abc123", PubKeyB64: "cGxhY2Vob2xkZXI="}
	if err := backend.Save(want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := backend.Load()
	if err != nil || !ok {
		t.Fatalf("expected a saved identity, got ok=%v err=%v", ok, err)
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestFileSelfIdentityBackendCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := writeGarbage(filepath.Join(dir, "nodes"), "self_identity.json"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewFileSelfIdentityBackend(dir).Load(); err == nil {
		t.Fatal("expected refusal for a corrupt self identity file")
	}
}

func TestFileControllerBindingBackend(t *testing.T) {
	dir := t.TempDir()
	backend := NewFileControllerBindingBackend(dir)
	if _, ok, err := backend.Load(); err != nil || ok {
		t.Fatalf("expected no binding yet, got ok=%v err=%v", ok, err)
	}
	nodesDir := filepath.Join(dir, "nodes")
	if err := writeGarbage(nodesDir, "controller_binding.json"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := backend.Load(); err == nil {
		t.Fatal("expected refusal for a corrupt controller binding file")
	}
	valid := []byte(`{"endpoint":"http://127.0.0.1:9999","enrollment_id":"e1"}`)
	if err := os.WriteFile(filepath.Join(nodesDir, "controller_binding.json"), valid, 0o600); err != nil {
		t.Fatal(err)
	}
	binding, ok, err := backend.Load()
	if err != nil || !ok {
		t.Fatalf("expected a valid binding, got ok=%v err=%v", ok, err)
	}
	if binding.Endpoint != "http://127.0.0.1:9999" || binding.EnrollmentID != "e1" {
		t.Fatalf("unexpected binding: %+v", binding)
	}
	emptyEndpoint := []byte(`{"endpoint":"","enrollment_id":"e1"}`)
	if err := os.WriteFile(filepath.Join(nodesDir, "controller_binding.json"), emptyEndpoint, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := backend.Load(); err != nil || ok {
		t.Fatalf("empty endpoint must resolve to ok=false, got ok=%v err=%v", ok, err)
	}
}

// TestNewSystemTicker drives the REAL production Ticker (not the test
// fake heartbeat_test.go uses) end to end: it fires and Stop is
// idempotent.
func TestNewSystemTicker(t *testing.T) {
	ticker := NewSystemTicker(10 * time.Millisecond)
	defer ticker.Stop()
	select {
	case <-ticker.C():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the system ticker to fire")
	}
	ticker.Stop()
	ticker.Stop() // idempotent
}
