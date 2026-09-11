package nodes

import (
	"os"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

type memKnownHostsBackend struct {
	m       map[string]string
	loadErr error
}

func newMemKnownHostsBackend() *memKnownHostsBackend {
	return &memKnownHostsBackend{m: map[string]string{}}
}

func (b *memKnownHostsBackend) Load() (map[string]string, error) {
	if b.loadErr != nil {
		return nil, b.loadErr
	}
	out := map[string]string{}
	for k, v := range b.m {
		out[k] = v
	}
	return out, nil
}

func (b *memKnownHostsBackend) Save(m map[string]string) error {
	b.m = m
	return nil
}

// TestVerifyUnknownHostRefused proves an unpinned host with no explicit
// fingerprint is refused (fail-closed).
func TestVerifyUnknownHostRefused(t *testing.T) {
	kh := NewKnownHosts(newMemKnownHostsBackend())
	err := kh.Verify("worker@host1", "abc123", "")
	if err == nil {
		t.Fatal("expected refusal for unknown host key")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindPermissionDenied {
		t.Fatalf("expected KindPermissionDenied, got %v (ok=%v)", k, ok)
	}
}

func TestVerifyUnknownHostAcceptedWithMatchingFingerprint(t *testing.T) {
	kh := NewKnownHosts(newMemKnownHostsBackend())
	if err := kh.Verify("worker@host1", "abc123", "abc123"); err != nil {
		t.Fatalf("unexpected error with matching explicit fingerprint: %v", err)
	}
}

func TestVerifyUnknownHostRefusedOnMismatch(t *testing.T) {
	kh := NewKnownHosts(newMemKnownHostsBackend())
	err := kh.Verify("worker@host1", "abc123", "different")
	if err == nil {
		t.Fatal("expected refusal when explicit fingerprint does not match presented")
	}
}

// TestVerifyChangedHostKeyRefused proves a previously-pinned host whose
// key changed is refused unconditionally, even with a matching
// --host-key-fingerprint (R-21.220: reconnect never silently re-pins).
func TestVerifyChangedHostKeyRefused(t *testing.T) {
	backend := newMemKnownHostsBackend()
	kh := NewKnownHosts(backend)
	if err := kh.Pin("worker@host1", "original-fp", false); err != nil {
		t.Fatal(err)
	}
	err := kh.Verify("worker@host1", "changed-fp", "changed-fp")
	if err == nil {
		t.Fatal("expected refusal for changed host key even with matching explicit fingerprint")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindPermissionDenied {
		t.Fatalf("expected KindPermissionDenied, got %v (ok=%v)", k, ok)
	}
}

func TestVerifyPinnedHostMatches(t *testing.T) {
	backend := newMemKnownHostsBackend()
	kh := NewKnownHosts(backend)
	if err := kh.Pin("worker@host1", "fp1", false); err != nil {
		t.Fatal(err)
	}
	if err := kh.Verify("worker@host1", "fp1", ""); err != nil {
		t.Fatalf("unexpected error for matching pinned key: %v", err)
	}
}

func TestPinChangedKeyRequiresForce(t *testing.T) {
	backend := newMemKnownHostsBackend()
	kh := NewKnownHosts(backend)
	if err := kh.Pin("worker@host1", "fp1", false); err != nil {
		t.Fatal(err)
	}
	if err := kh.Pin("worker@host1", "fp2", false); err == nil {
		t.Fatal("expected refusal re-pinning a changed key without force")
	}
	if err := kh.Pin("worker@host1", "fp2", true); err != nil {
		t.Fatalf("unexpected error re-pinning with force: %v", err)
	}
	if err := kh.Verify("worker@host1", "fp2", ""); err != nil {
		t.Fatalf("expected fp2 to now be pinned: %v", err)
	}
}

func TestHostKeyFingerprintDeterministic(t *testing.T) {
	key := []byte("a-fake-host-public-key-bytes")
	fp1 := HostKeyFingerprint(key)
	fp2 := HostKeyFingerprint(key)
	if fp1 != fp2 {
		t.Fatal("fingerprint must be deterministic")
	}
	if fp1 == HostKeyFingerprint([]byte("different-key")) {
		t.Fatal("distinct keys must produce distinct fingerprints")
	}
}

func TestKnownHostsBackendUnavailable(t *testing.T) {
	backend := newMemKnownHostsBackend()
	backend.loadErr = cascade.New(cascade.KindUnavailable, "simulated outage")
	kh := NewKnownHosts(backend)
	err := kh.Verify("worker@host1", "abc", "abc")
	if err == nil {
		t.Fatal("expected error when backend unavailable")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindUnavailable {
		t.Fatalf("expected KindUnavailable, got %v (ok=%v)", k, ok)
	}
}

func TestFileKnownHostsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	kh := NewKnownHosts(NewFileKnownHostsBackend(dir))
	if err := kh.Pin("worker@host1", "fp1", false); err != nil {
		t.Fatal(err)
	}
	kh2 := NewKnownHosts(NewFileKnownHostsBackend(dir))
	if err := kh2.Verify("worker@host1", "fp1", ""); err != nil {
		t.Fatalf("unexpected error reading persisted pin: %v", err)
	}
}

func TestFileKnownHostsCorruptJSON(t *testing.T) {
	dir := t.TempDir()
	if err := writeGarbage(dir+"/nodes", "known_hosts.json"); err != nil {
		t.Fatal(err)
	}
	// Exercise the backend's own Load directly: it reports KindIntegrity
	// for a corrupt store. KnownHosts.Verify (tested via Load's caller,
	// below) re-wraps any backend error as KindUnavailable, which is a
	// distinct assertion.
	_, err := NewFileKnownHostsBackend(dir).Load()
	if err == nil {
		t.Fatal("expected error loading corrupt known_hosts store")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindIntegrity {
		t.Fatalf("expected KindIntegrity, got %v (ok=%v)", k, ok)
	}

	kh := NewKnownHosts(NewFileKnownHostsBackend(dir))
	verifyErr := kh.Verify("host", "fp", "fp")
	if verifyErr == nil {
		t.Fatal("expected Verify to also refuse over a corrupt store")
	}
	if k, ok := cascade.KindOf(verifyErr); !ok || k != cascade.KindUnavailable {
		t.Fatalf("expected KindUnavailable from Verify, got %v (ok=%v)", k, ok)
	}
}

func TestPinBackendUnavailable(t *testing.T) {
	backend := newMemKnownHostsBackend()
	backend.loadErr = cascade.New(cascade.KindUnavailable, "simulated outage")
	kh := NewKnownHosts(backend)
	if err := kh.Pin("host", "fp", false); err == nil {
		t.Fatal("expected error when backend unavailable")
	}
}

type saveErrKnownHostsBackend struct {
	*memKnownHostsBackend
	saveErr error
}

func (b *saveErrKnownHostsBackend) Save(m map[string]string) error {
	if b.saveErr != nil {
		return b.saveErr
	}
	return b.memKnownHostsBackend.Save(m)
}

func TestPinSaveErrorPropagates(t *testing.T) {
	backend := &saveErrKnownHostsBackend{
		memKnownHostsBackend: newMemKnownHostsBackend(),
		saveErr:              cascade.New(cascade.KindUnavailable, "simulated write failure"),
	}
	kh := NewKnownHosts(backend)
	if err := kh.Pin("host", "fp", false); err == nil {
		t.Fatal("expected error when backend save fails")
	}
}

// TestFileKnownHostsLoad_UnreadableNotNotExist: a directory at the
// expected file path makes os.ReadFile fail with something other than
// IsNotExist, which fileKnownHostsBackend.Load must propagate rather
// than swallowing into an empty map.
func TestFileKnownHostsLoad_UnreadableNotNotExist(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/nodes/known_hosts.json", 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileKnownHostsBackend(dir).Load(); err == nil {
		t.Fatal("expected a read error for an unreadable (directory) store path")
	}
}

// TestFileKnownHostsLoad_NullJSONBecomesEmptyMap proves a store file
// whose content is the JSON literal "null" (valid JSON, decodes to a nil
// map) resolves to an empty, non-nil map, not a nil one a caller might
// panic writing into.
func TestFileKnownHostsLoad_NullJSONBecomesEmptyMap(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/nodes", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/nodes/known_hosts.json", []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := NewFileKnownHostsBackend(dir).Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected a non-nil empty map for JSON null content")
	}
	if len(m) != 0 {
		t.Fatalf("expected an empty map, got %d entries", len(m))
	}
}
