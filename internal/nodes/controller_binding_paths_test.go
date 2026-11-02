// Purpose: error-path tests for the file-backed controller binding store:
// an unusable nodes directory, a conflicting file that cannot be read or
// holds no binding, and the failure branches of writeSynced.
// SPORT: nodes tests (EXTEND) - P1-CI-13.
package nodes

import (
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func wantFailure(t *testing.T, err error, kind cascade.Kind, text string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %v containing %q, got nil", kind, text)
	}
	wantKind(t, err, kind)
	if !strings.Contains(err.Error(), text) {
		t.Fatalf("error %q does not contain %q", err, text)
	}
}

func TestControllerBindingSaveFilesystemRefusals(t *testing.T) {
	binding := ControllerBinding{Endpoint: "http://c.invalid", EnrollmentID: "enr", ControllerPubKeyB64: testControllerKeyB64(t)}
	t.Run("nodes directory path is a regular file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "nodes"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		wantFailure(t, NewFileControllerBindingBackend(dir).Save(binding), cascade.KindUnavailable, "create the nodes directory")
	})
	existing := func(t *testing.T, content string) error {
		t.Helper()
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "nodes"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "nodes", "controller_binding.json"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return NewFileControllerBindingBackend(dir).Save(binding)
	}
	t.Run("existing file is not JSON", func(t *testing.T) {
		wantFailure(t, existing(t, "not json"), cascade.KindIntegrity, "not valid JSON")
	})
	t.Run("existing file holds no binding", func(t *testing.T) {
		wantFailure(t, existing(t, "{}"), cascade.KindInvalidInput, "empty or malformed")
	})
}

func TestWriteSyncedFailures(t *testing.T) {
	t.Run("write to a closed file", func(t *testing.T) {
		f, err := os.Create(filepath.Join(t.TempDir(), "closed"))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		err = writeSynced(f, []byte("data"))
		if err == nil || !strings.Contains(err.Error(), "closed") {
			t.Fatalf("got %v, want a file-already-closed error", err)
		}
	})
	t.Run("sync on a pipe is rejected by posix kernels", func(t *testing.T) {
		if goruntime.GOOS == "windows" {
			t.Skip("FlushFileBuffers accepts a pipe handle on this platform")
		}
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = r.Close() }()
		err = writeSynced(w, []byte("data"))
		if err == nil || !strings.Contains(err.Error(), "sync") {
			t.Fatalf("got %v, want an fsync error", err)
		}
	})
}
