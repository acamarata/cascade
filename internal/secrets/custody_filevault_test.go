package secrets

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// newTestFileVault builds a file vault under t.TempDir() with a fixed
// passphrase, so no test touches the real user's vault or a shared path.
func newTestFileVault(t *testing.T) *fileVaultCustody {
	t.Helper()
	fv, err := newFileVaultCustody(Config{Service: "cascade-test", Dir: t.TempDir(), Passphrase: "test-pass"})
	if err != nil {
		t.Fatalf("building the file vault: %v", err)
	}
	return fv
}

func TestFileVaultRoundTrip(t *testing.T) {
	fv := newTestFileVault(t)
	ctx := context.Background()
	if !fv.Available() {
		t.Fatal("a writable temp dir reported unavailable")
	}
	if fv.Name() != fileVaultName {
		t.Fatalf("Name() = %q", fv.Name())
	}
	if err := fv.Set(ctx, "TOKEN", []byte("s3cr3t")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := fv.Get(ctx, "TOKEN")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, []byte("s3cr3t")) {
		t.Fatalf("Get returned %d bytes, not the stored value", len(got))
	}
	// Idempotent overwrite: a second Set of the same name succeeds and
	// leaves exactly one entry.
	if err := fv.Set(ctx, "TOKEN", []byte("rotated")); err != nil {
		t.Fatalf("second Set: %v", err)
	}
	names, err := fv.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 1 || names[0] != "TOKEN" {
		t.Fatalf("List = %v, want exactly [TOKEN]", names)
	}
	if err := fv.Delete(ctx, "TOKEN"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := fv.Get(ctx, "TOKEN"); !isKind(err, cascade.KindNotFound) {
		t.Fatalf("Get after Delete = %v, want a not-found refusal", err)
	}
	if err := fv.Delete(ctx, "TOKEN"); !isKind(err, cascade.KindNotFound) {
		t.Fatalf("second Delete = %v, want a not-found refusal", err)
	}
}

func TestFileVaultCiphertextOnDiskHidesTheValue(t *testing.T) {
	fv := newTestFileVault(t)
	if err := fv.Set(context.Background(), "TOKEN", []byte("plaintext-canary")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(fv.dir, fileVaultFileName))
	if err != nil {
		t.Fatalf("reading the vault file: %v", err)
	}
	if bytes.Contains(raw, []byte("plaintext-canary")) {
		t.Fatal("the stored value appears verbatim in the vault file")
	}
	if !bytes.HasPrefix(raw, []byte(ageIntro)) {
		t.Fatal("the vault file is not an age file")
	}
}

func TestFileVaultRefusesCorruptStore(t *testing.T) {
	fv := newTestFileVault(t)
	ctx := context.Background()
	if err := fv.Set(ctx, "TOKEN", []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	path := filepath.Join(fv.dir, fileVaultFileName)
	if err := os.WriteFile(path, []byte("not an age file"), 0o600); err != nil {
		t.Fatalf("corrupting the store: %v", err)
	}
	for name, call := range map[string]func() error{
		"List":   func() error { _, err := fv.List(ctx); return err },
		"Get":    func() error { _, err := fv.Get(ctx, "TOKEN"); return err },
		"Set":    func() error { return fv.Set(ctx, "TOKEN", []byte("v")) },
		"Delete": func() error { return fv.Delete(ctx, "TOKEN") },
	} {
		if err := call(); !isKind(err, cascade.KindIntegrity) {
			t.Fatalf("%s over a corrupt store = %v, want an integrity refusal", name, err)
		}
	}
	// The refusal must not have replaced the file with an empty vault.
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "not an age file" {
		t.Fatalf("the corrupt store was overwritten: %q, %v", raw, err)
	}
}

func TestFileVaultRefusesNonMapContents(t *testing.T) {
	fv := newTestFileVault(t)
	sealed, err := ageEncrypt(fv.passphrase, []byte(`["not","a","map"]`), 2, fv.rnd)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fv.dir, fileVaultFileName), sealed, 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if _, err := fv.List(context.Background()); !isKind(err, cascade.KindIntegrity) {
		t.Fatalf("List over a non-map store = %v, want an integrity refusal", err)
	}
}

func TestFileVaultValidatesNames(t *testing.T) {
	fv := newTestFileVault(t)
	ctx := context.Background()
	if err := fv.Set(ctx, "bad name", []byte("v")); !isKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Set with a bad name = %v", err)
	}
	if _, err := fv.Get(ctx, ""); !isKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Get with an empty name = %v", err)
	}
	if err := fv.Delete(ctx, "bad name"); !isKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Delete with a bad name = %v", err)
	}
}

func TestFileVaultKeyFileLifecycle(t *testing.T) {
	dir := t.TempDir()
	first, err := newFileVaultCustody(Config{Service: "s", Dir: dir})
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := first.Set(context.Background(), "K", []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, fileVaultKeyName))
	if err != nil {
		t.Fatalf("the key file was not created: %v", err)
	}
	if info.Mode().Perm() != fileVaultFilePerm {
		t.Fatalf("key file mode = %v, want %v", info.Mode().Perm(), fileVaultFilePerm)
	}
	// A second open reuses the same key file, so the stored secret is
	// still readable. A regenerated key would silently orphan the vault.
	second, err := newFileVaultCustody(Config{Service: "s", Dir: dir})
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	got, err := second.Get(context.Background(), "K")
	if err != nil || string(got) != "v" {
		t.Fatalf("reopened vault Get = %q, %v", got, err)
	}
}

func TestFileVaultRefusesShortKeyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, fileVaultKeyName), []byte("short"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if _, err := newFileVaultCustody(Config{Service: "s", Dir: dir}); !isKind(err, cascade.KindIntegrity) {
		t.Fatalf("a truncated key file was accepted: %v", err)
	}
}

func TestFileVaultConstructionRefusals(t *testing.T) {
	if _, err := newFileVaultCustody(Config{Service: "s"}); !isKind(err, cascade.KindInvalidInput) {
		t.Fatalf("an empty dir was accepted: %v", err)
	}
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if _, err := newFileVaultCustody(Config{Service: "s", Dir: filepath.Join(file, "sub")}); !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("an unusable dir was accepted: %v", err)
	}
	if _, err := newFileVaultCustody(Config{Service: "s", Dir: t.TempDir(), Rand: errReader{}}); !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("a failing entropy source was accepted: %v", err)
	}
}

func TestFileVaultAvailableFalseOnUnwritableDir(t *testing.T) {
	dir := t.TempDir()
	fv, err := newFileVaultCustody(Config{Service: "s", Dir: dir, Passphrase: "p"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if fv.Available() {
		t.Fatal("a read-only directory reported available")
	}
}

func TestSelectCustodyFallsBackToFileVault(t *testing.T) {
	// A runner that fails every invocation makes the darwin backend report
	// unavailable; on the other platforms there is no native backend to
	// begin with. Either way the selector must land on the file vault and
	// say so, rather than returning a store that quietly drops writes.
	custody, err := SelectCustody(Config{
		Service: "cascade-test",
		Dir:     t.TempDir(),
		Runner:  failingRunner,
	})
	if err != nil {
		t.Fatalf("SelectCustody: %v", err)
	}
	if custody.Name() != fileVaultName {
		t.Fatalf("selected %q, want the file vault", custody.Name())
	}
	ctx := context.Background()
	if err := custody.Set(ctx, "A", []byte("1")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := custody.Get(ctx, "A")
	if err != nil || string(got) != "1" {
		t.Fatalf("round trip through the fallback = %q, %v", got, err)
	}
}

func TestSelectCustodyRefusesWithoutAnyBackend(t *testing.T) {
	if _, err := SelectCustody(Config{Dir: t.TempDir()}); !isKind(err, cascade.KindInvalidInput) {
		t.Fatalf("a config with no service label was accepted: %v", err)
	}
	if _, err := SelectCustody(Config{Service: "s", Runner: failingRunner}); !isKind(err, cascade.KindInvalidInput) {
		t.Fatalf("a config with no vault dir was accepted: %v", err)
	}
}

func TestWriteFilePrivateRefusesUnusableDir(t *testing.T) {
	err := writeFilePrivate(filepath.Join(t.TempDir(), "missing", "f"), []byte("x"))
	if err == nil {
		t.Fatal("writing into a missing directory succeeded")
	}
	if strings.Contains(err.Error(), "x") && len(err.Error()) < 3 {
		t.Fatal("the error echoed the payload")
	}
}
