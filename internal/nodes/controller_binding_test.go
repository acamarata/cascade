package nodes

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// testControllerKeyB64 returns a fresh controller public key, base64.
func testControllerKeyB64(t *testing.T) string {
	t.Helper()
	id, _, err := GenerateIdentity(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return id.PubKeyB64()
}

func wantKind(t *testing.T, err error, want cascade.Kind) {
	t.Helper()
	if kind, ok := cascade.KindOf(err); !ok || kind != want {
		t.Fatalf("got %v (kind %v, ok=%v), want %v", err, kind, ok, want)
	}
}

// assertMalformedExistingKeyInvalid checks Save over a file holding a malformed key is invalid input.
func assertMalformedExistingKeyInvalid(t *testing.T, binding ControllerBinding) {
	t.Helper()
	bad := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bad, "nodes"), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"endpoint":"e","enrollment_id":"i","controller_pubkey_b64":"%%%"}`)
	if err := os.WriteFile(filepath.Join(bad, "nodes", "controller_binding.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	wantKind(t, NewFileControllerBindingBackend(bad).Save(binding), cascade.KindInvalidInput)
}

// assertConcurrentSavesOneWinner checks racing saves of different keys leave exactly one winner.
func assertConcurrentSavesOneWinner(t *testing.T, binding ControllerBinding) {
	t.Helper()
	race := NewFileControllerBindingBackend(t.TempDir())
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		b := binding
		b.ControllerPubKeyB64 = testControllerKeyB64(t)
		go func() { errs <- race.Save(b) }()
	}
	wins := 0
	for i := 0; i < 8; i++ {
		if err := <-errs; err == nil {
			wins++
		} else {
			wantKind(t, err, cascade.KindConflict)
		}
	}
	if wins != 1 {
		t.Fatalf("%d saves succeeded, want exactly 1", wins)
	}
}

// assertMalformedInputRefused checks malformed bindings are refused before any write.
func assertMalformedInputRefused(t *testing.T, binding ControllerBinding) {
	t.Helper()
	fresh := NewFileControllerBindingBackend(t.TempDir())
	short := base64.StdEncoding.EncodeToString(make([]byte, 31))
	for name, b := range map[string]ControllerBinding{
		"short key":     {Endpoint: "e", EnrollmentID: "i", ControllerPubKeyB64: short},
		"not base64":    {Endpoint: "e", EnrollmentID: "i", ControllerPubKeyB64: "%%%"},
		"empty key":     {Endpoint: "e", EnrollmentID: "i"},
		"no endpoint":   {EnrollmentID: "i", ControllerPubKeyB64: binding.ControllerPubKeyB64},
		"no enrollment": {Endpoint: "e", ControllerPubKeyB64: binding.ControllerPubKeyB64},
	} {
		wantKind(t, fresh.Save(b), cascade.KindInvalidInput)
		if _, ok, _ := fresh.Load(); ok {
			t.Fatalf("%s: a refused save wrote a binding", name)
		}
	}
}

func TestControllerBindingSaveWriteOnce(t *testing.T) {
	dir := t.TempDir()
	store := NewFileControllerBindingBackend(dir)
	binding := ControllerBinding{Endpoint: "http://controller.invalid:7443", EnrollmentID: "enr-1", ControllerPubKeyB64: testControllerKeyB64(t)}
	if err := store.Save(binding); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "nodes", "controller_binding.json")
	t.Run("file modes", func(t *testing.T) {
		if goruntime.GOOS == "windows" {
			t.Skip("POSIX permission bits do not apply on this platform")
		}
		for p, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
			info, err := os.Stat(p)
			if err != nil || info.Mode().Perm() != want {
				t.Fatalf("%s mode %v (err %v), want %v", p, info.Mode().Perm(), err, want)
			}
		}
	})
	got, ok, err := store.Load()
	if err != nil || !ok || got != binding {
		t.Fatalf("load %+v %v %v, want %+v", got, ok, err, binding)
	}
	t.Run("equal binding is a no-op", func(t *testing.T) {
		if err := store.Save(binding); err != nil {
			t.Fatalf("equal save: %v", err)
		}
	})
	t.Run("different key conflicts and changes nothing", func(t *testing.T) {
		other := binding
		other.ControllerPubKeyB64 = testControllerKeyB64(t)
		wantKind(t, store.Save(other), cascade.KindConflict)
		if got, _, _ := store.Load(); got != binding {
			t.Fatalf("binding replaced: %+v", got)
		}
	})
	t.Run("existing file with a malformed key is invalid input", func(t *testing.T) {
		assertMalformedExistingKeyInvalid(t, binding)
	})
	t.Run("concurrent saves of different keys leave exactly one winner", func(t *testing.T) {
		assertConcurrentSavesOneWinner(t, binding)
	})
	t.Run("different endpoint under the same key conflicts", func(t *testing.T) {
		other := binding
		other.Endpoint = "http://elsewhere.invalid:7443"
		wantKind(t, store.Save(other), cascade.KindConflict)
	})
	t.Run("malformed input is refused before any write", func(t *testing.T) {
		assertMalformedInputRefused(t, binding)
	})
}

func TestControllerBindingLoadWithoutKey(t *testing.T) {
	dir := t.TempDir()
	nodesDir := filepath.Join(dir, "nodes")
	if err := os.MkdirAll(nodesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"endpoint":"http://c.invalid","enrollment_id":"e1"}`)
	if err := os.WriteFile(filepath.Join(nodesDir, "controller_binding.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok, err := NewFileControllerBindingBackend(dir).Load()
	if err != nil || !ok || got.ControllerPubKeyB64 != "" {
		t.Fatalf("load %+v %v %v", got, ok, err)
	}
	wantKind(t, VerifyControllerRequest(got, []byte("p"), make([]byte, 64)), cascade.KindPermissionDenied)
}
