package learn

// Purpose: the salt file's hardening: a 0-byte or symlinked salt is refused
//   and never followed or overwritten, a loose salt directory is refused with
//   nothing created, and concurrent creators converge on one complete salt
//   with no temp file left behind.
// SPORT: internal.learn.EnsureAggregateSalt/TESTED (P1-CAP-03).

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestAggregateSaltRefusesEmptyAndSymlink: a 0-byte file is KindIntegrity and
// stays 0 bytes; a symlink to a valid 32-byte file, and a dangling symlink,
// are KindIntegrity, never followed and never replaced. The loader refuses
// the same files.
func TestAggregateSaltRefusesEmptyAndSymlink(t *testing.T) {
	skipIfNoModeBits(t)
	empty := saltFileWith(t, nil)
	if _, err := EnsureAggregateSalt(empty); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Errorf("0-byte salt: EnsureAggregateSalt = %v, want KindIntegrity", err)
	}
	if fi, err := os.Stat(empty); err != nil || fi.Size() != 0 {
		t.Errorf("0-byte salt after refusal: %v, %v, want it untouched", fi, err)
	}
	valid := saltFileWith(t, bytes.Repeat([]byte{9}, 32))
	for name, target := range map[string]string{"valid target": valid, "dangling": filepath.Join(t.TempDir(), "nowhere")} {
		path := saltPathIn(t)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		if _, err := EnsureAggregateSalt(path); !cascade.HasKind(err, cascade.KindIntegrity) {
			t.Errorf("symlink to %s: EnsureAggregateSalt = %v, want KindIntegrity", name, err)
		}
		if _, err := loadSalt(path); !cascade.HasKind(err, cascade.KindIntegrity) {
			t.Errorf("symlink to %s: loadSalt = %v, want KindIntegrity", name, err)
		}
		if got, err := os.Readlink(path); err != nil || got != target {
			t.Errorf("symlink to %s was replaced: %q, %v", name, got, err)
		}
	}
}

// TestAggregateSaltRefusesLooseDirectory: an existing salt directory that is
// group or world accessible is KindPermissionDenied and no salt is created
// in it; the loader refuses an existing salt inside one too.
func TestAggregateSaltRefusesLooseDirectory(t *testing.T) {
	skipIfNoModeBits(t)
	path := saltPathIn(t)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureAggregateSalt(path); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Errorf("0755 salt dir: EnsureAggregateSalt = %v, want KindPermissionDenied", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("salt dir holds %d entries after the refusal, want 0", len(entries))
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte{3}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSalt(path); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Errorf("valid salt in a 0755 dir: loadSalt = %v, want KindPermissionDenied", err)
	}
}

// TestAggregateSaltConcurrentCreate: 16 concurrent EnsureAggregateSalt calls
// on one fresh path all return the same 32 bytes, those bytes are the file's
// content, and the directory holds the salt alone (no temp file).
func TestAggregateSaltConcurrentCreate(t *testing.T) {
	path := saltPathIn(t)
	const callers = 16
	salts, errs := make([][]byte, callers), make([]error, callers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			salts[i], errs[i] = EnsureAggregateSalt(path)
		}(i)
	}
	close(start)
	wg.Wait()
	onDisk, err := os.ReadFile(path)
	if err != nil || len(onDisk) != 32 {
		t.Fatalf("salt file = %x, %v, want 32 bytes", onDisk, err)
	}
	for i := range salts {
		if errs[i] != nil || !bytes.Equal(salts[i], onDisk) {
			t.Errorf("caller %d: salt %x, err %v, want the file's %x", i, salts[i], errs[i], onDisk)
		}
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("salt dir holds %d entries, want exactly the salt", len(entries))
	}
}
