package nodes

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestReadWithFallback pins the long-path guard: a primary that reports
// not-exist defers to os.ReadFile, whose answer is final; any other
// primary result is returned untouched.
func TestReadWithFallback(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "devices.json")
	if err := os.WriteFile(present, []byte(`{"n1":{}}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	missing := filepath.Join(dir, "absent.json")
	notExist := func(p string) ([]byte, error) {
		return nil, &os.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	hard := errors.New("hard read failure")

	t.Run("not-exist on an existing file takes the fallback", func(t *testing.T) {
		got, err := readWithFallback(notExist, present)
		if err != nil || string(got) != `{"n1":{}}` {
			t.Fatalf("got %q, %v; want the file content from os.ReadFile", got, err)
		}
	})
	t.Run("a really missing file stays not-exist", func(t *testing.T) {
		got, err := readWithFallback(notExist, missing)
		if !errors.Is(err, fs.ErrNotExist) || got != nil {
			t.Fatalf("got %q, %v; want fs.ErrNotExist", got, err)
		}
	})
	t.Run("any other error is returned without a fallback", func(t *testing.T) {
		got, err := readWithFallback(func(string) ([]byte, error) { return nil, hard }, present)
		if !errors.Is(err, hard) || got != nil {
			t.Fatalf("got %q, %v; want the primary's error unchanged", got, err)
		}
	})
	t.Run("primary success is returned as is", func(t *testing.T) {
		got, err := readWithFallback(func(string) ([]byte, error) { return []byte("primary"), nil }, missing)
		if err != nil || string(got) != "primary" {
			t.Fatalf("got %q, %v; want the primary's bytes", got, err)
		}
	})
}

// lockWait bounds how long the test waits to call an operation blocked.
// A blocked op never finishes, so the window only costs time, never
// correctness; a short one is enough to catch an op that ignores the lock.
const lockWait = 50 * time.Millisecond

// TestFileRecordBackend_LockSerializes pins the in-process layer: Load
// waits while a writer holds the path's lock, and Save waits while a
// reader holds it. Removing either Lock call makes this fail (mutation
// recorded in .claude/evidence/WINFLAKE/lock-mutation.txt).
func TestFileRecordBackend_LockSerializes(t *testing.T) {
	dataDir := t.TempDir()
	b := NewFileRecordBackend(dataDir)
	if err := b.Save(map[string]DeviceRecord{"n1": {NodeID: "n1"}}); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	mu := recordFileLock(filepath.Join(dataDir, "nodes", "devices.json"))

	t.Run("Load waits for a writer", func(t *testing.T) {
		mu.Lock()
		assertBlockedUntil(t, mu.Unlock, func() error { _, err := b.Load(); return err })
	})
	t.Run("Save waits for a reader", func(t *testing.T) {
		mu.RLock()
		assertBlockedUntil(t, mu.RUnlock, func() error { return b.Save(map[string]DeviceRecord{"n2": {NodeID: "n2"}}) })
	})
}

// assertBlockedUntil runs op in a goroutine while the caller holds the
// lock, requires op to still be running after lockWait, then calls
// release and requires op to finish without error.
func assertBlockedUntil(t *testing.T, release func(), op func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- op() }()
	select {
	case err := <-done:
		release()
		t.Fatalf("op finished (err=%v) while the lock was held: the lock does not serialize it", err)
	case <-time.After(lockWait):
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("op after release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("op never finished after the lock was released")
	}
}
