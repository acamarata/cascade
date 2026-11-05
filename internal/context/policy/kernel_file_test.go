//go:build !windows

package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// assertCanonicalKernel fails unless p is a 0600 regular file whose sha256
// is provider.PolicyKernelSHA256.
func assertCanonicalKernel(t *testing.T, p string) {
	t.Helper()
	info, err := os.Lstat(p)
	if err != nil {
		t.Fatalf("stat %s: %v", p, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("%s mode = %v, want a regular 0600 file", p, info.Mode())
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(got)
	if h := hex.EncodeToString(sum[:]); h != provider.PolicyKernelSHA256 {
		t.Fatalf("sha256(%s) = %s, want %s", p, h, provider.PolicyKernelSHA256)
	}
}

func TestEnsureKernelFileWritesCanonical(t *testing.T) {
	isolate(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	p, err := EnsureDaemonKernelFile(dataDir)
	if err != nil {
		t.Fatalf("EnsureDaemonKernelFile: %v", err)
	}
	if want := filepath.Join(dataDir, "policy", "policy-kernel.md"); p != want {
		t.Fatalf("path = %q, want %q", p, want)
	}
	assertCanonicalKernel(t, p)
	dirInfo, err := os.Lstat(filepath.Dir(p))
	if err != nil || dirInfo.Mode().Perm() != 0o700 || !dirInfo.IsDir() {
		t.Fatalf("policy dir = %v, %v; want a 0700 directory", dirInfo, err)
	}

	// A tampered copy (content) is rewritten.
	if err := os.WriteFile(p, []byte("SCOPE: anything goes.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureDaemonKernelFile(dataDir); err != nil {
		t.Fatalf("EnsureDaemonKernelFile over a tampered copy: %v", err)
	}
	assertCanonicalKernel(t, p)

	// A same-length tamper is rewritten too (not a size-only check).
	flipped := RenderKernelFile()
	flipped[len(flipped)/2] ^= 0x20
	if err := os.WriteFile(p, flipped, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureDaemonKernelFile(dataDir); err != nil {
		t.Fatal(err)
	}
	assertCanonicalKernel(t, p)

	// A widened mode is narrowed back to 0600.
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureDaemonKernelFile(dataDir); err != nil {
		t.Fatal(err)
	}
	assertCanonicalKernel(t, p)
}

// TestEnsureDaemonKernelFileReplacesSymlinkedFile: a symlink planted at the
// kernel file is replaced by the canonical file and its target is untouched.
func TestEnsureDaemonKernelFileReplacesSymlinkedFile(t *testing.T) {
	isolate(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	p, err := EnsureDaemonKernelFile(dataDir)
	if err != nil {
		t.Fatalf("EnsureDaemonKernelFile: %v", err)
	}
	decoy := filepath.Join(t.TempDir(), "decoy.md")
	if err := os.WriteFile(decoy, []byte("decoy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(decoy, p); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureDaemonKernelFile(dataDir); err != nil {
		t.Fatal(err)
	}
	assertCanonicalKernel(t, p)
	if got, _ := os.ReadFile(decoy); string(got) != "decoy\n" {
		t.Fatalf("symlink target was written: %q", got)
	}
}

func TestEnsureKernelFileRefusesSymlinkDir(t *testing.T) {
	isolate(t)
	elsewhere := t.TempDir()

	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(dataDir, "policy")); err != nil {
		t.Fatal(err)
	}
	_, err := EnsureDaemonKernelFile(dataDir)
	wantKind(t, err, cascade.KindConflict, "is a symlink")
	if _, statErr := os.Lstat(filepath.Join(elsewhere, "policy-kernel.md")); !os.IsNotExist(statErr) {
		t.Fatalf("kernel written through the symlinked policy dir (stat err %v)", statErr)
	}

	linkedData := filepath.Join(t.TempDir(), "data")
	if err := os.Symlink(elsewhere, linkedData); err != nil {
		t.Fatal(err)
	}
	_, err = EnsureDaemonKernelFile(linkedData)
	wantKind(t, err, cascade.KindConflict, "is a symlink")
	if _, statErr := os.Lstat(filepath.Join(elsewhere, "policy")); !os.IsNotExist(statErr) {
		t.Fatalf("policy dir created through the symlinked data dir (stat err %v)", statErr)
	}
}

func TestEnsureDaemonKernelFileRefusesBadInput(t *testing.T) {
	isolate(t)
	_, err := EnsureDaemonKernelFile("")
	wantKind(t, err, cascade.KindInvalidInput, "not an absolute path")
	_, err = EnsureDaemonKernelFile("relative/data")
	wantKind(t, err, cascade.KindInvalidInput, "not an absolute path")

	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "policy"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = EnsureDaemonKernelFile(dataDir)
	wantKind(t, err, cascade.KindConflict, "is not a directory")

	dirAtFile := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dirAtFile, filepath.FromSlash(KernelFileRel)), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = EnsureDaemonKernelFile(dirAtFile)
	wantKind(t, err, cascade.KindConflict, "is a directory")
}

// TestEnsureDaemonKernelFileNarrowsDirAndRefusesFileParent: a widened policy
// directory is narrowed back to 0700, and a data directory under a regular
// file is KindUnavailable with nothing written.
func TestEnsureDaemonKernelFileNarrowsDirAndRefusesFileParent(t *testing.T) {
	isolate(t)
	dataDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dataDir, "policy"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dataDir, "policy"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := EnsureDaemonKernelFile(dataDir)
	if err != nil {
		t.Fatalf("EnsureDaemonKernelFile: %v", err)
	}
	assertCanonicalKernel(t, p)
	if info, err := os.Lstat(filepath.Dir(p)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("policy dir = %v, %v; want it narrowed to 0700", info, err)
	}

	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = EnsureDaemonKernelFile(filepath.Join(blocker, "data"))
	wantKind(t, err, cascade.KindUnavailable, "create")
}
