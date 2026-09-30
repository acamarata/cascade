//go:build linux

package secrets

// Purpose: behaviour tests for error paths in the grant store, the file
//   vault and the vault.env scanner that every OS reaches but that no other
//   test drove. Each asserts the Kind and the exact message.
// Constraints: only temp dirs, nothing platform specific, no build tags.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// wantKind fails the test unless err carries kind and the exact message.
func wantKind(t *testing.T, err error, kind cascade.Kind, msg string) {
	t.Helper()
	if err == nil {
		t.Fatalf("got nil error, want %v %q", kind, msg)
	}
	if got, ok := cascade.KindOf(err); !ok || got != kind {
		t.Fatalf("Kind = %v (ok=%v), want %v; error %q", got, ok, kind, err)
	}
	if want := fmt.Sprintf("%v: %s", kind, msg); !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("message = %q, want prefix %q", err.Error(), want)
	}
}

func TestNewFileGrantStoreRefusesEmptyDir(t *testing.T) {
	store, err := NewFileGrantStore("")
	if store != nil {
		t.Fatalf("store = %v, want nil", store)
	}
	wantKind(t, err, cascade.KindInvalidInput, "secrets: a grant store needs a directory")
}

// TestFileGrantStoreLoadRefusesUnreadableRegister: a register path that
// exists but cannot be read is unavailable, never an empty register.
func TestFileGrantStoreLoadRefusesUnreadableRegister(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, grantsFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileGrantStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := store.LoadGrants(context.Background())
	if grants != nil {
		t.Fatalf("grants = %v, want nil", grants)
	}
	wantKind(t, err, cascade.KindUnavailable, "secrets: reading the grant register")
}

// TestFileGrantStoreSaveRefusesBlockedDirectory: the vault directory cannot
// be created below a regular file.
func TestFileGrantStoreSaveRefusesBlockedDirectory(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileGrantStore(filepath.Join(blocker, "vault"))
	if err != nil {
		t.Fatal(err)
	}
	err = store.SaveGrants(context.Background(), []Grant{{ID: "g1"}})
	wantKind(t, err, cascade.KindUnavailable, "secrets: creating the vault directory")
}

// TestFileGrantStoreSaveRefusesUnreplaceableRegister: the rename onto a
// non-empty directory fails, and no temp file is left behind.
func TestFileGrantStoreSaveRefusesUnreplaceableRegister(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, grantsFileName)
	if err := os.MkdirAll(filepath.Join(target, "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileGrantStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = store.SaveGrants(context.Background(), []Grant{{ID: "g1"}})
	wantKind(t, err, cascade.KindUnavailable, "secrets: replacing the grant register")
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(entries) != 1 || entries[0].Name() != grantsFileName {
		t.Fatalf("directory holds %d entries after a failed save, want only %s", len(entries), grantsFileName)
	}
}

// TestFileVaultKeyFileTooShortIsCorrupt: a truncated key file is refused,
// not padded or regenerated.
func TestFileVaultKeyFileTooShortIsCorrupt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, fileVaultKeyName), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	fv, err := newFileVaultCustody(Config{Service: "svc", Dir: dir})
	if fv != nil {
		t.Fatalf("vault = %v, want nil", fv)
	}
	wantKind(t, err, cascade.KindIntegrity, "secrets: the file-vault custody store could not be decoded")
	raw, rerr := os.ReadFile(filepath.Join(dir, fileVaultKeyName))
	if rerr != nil || string(raw) != "short" {
		t.Fatalf("key file was changed: %q, %v", raw, rerr)
	}
}

// TestFileVaultKeyPathUnreadableIsUnavailable: a key path that cannot be
// read for a reason other than absence is unavailable.
func TestFileVaultKeyPathUnreadableIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, fileVaultKeyName), 0o700); err != nil {
		t.Fatal(err)
	}
	fv, err := newFileVaultCustody(Config{Service: "svc", Dir: dir})
	if fv != nil {
		t.Fatalf("vault = %v, want nil", fv)
	}
	wantKind(t, err, cascade.KindUnavailable, "secrets: the file-vault custody backend is not available on this host")
}

// TestFileVaultUndecryptableStoreIsCorrupt: a vault file that is not a valid
// age file is refused on read and left untouched.
func TestFileVaultUndecryptableStoreIsCorrupt(t *testing.T) {
	dir := t.TempDir()
	fv, err := newFileVaultCustody(Config{Service: "svc", Dir: dir, Passphrase: "linuxreach-pass" + "phrase"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fileVaultFileName)
	if err := os.WriteFile(path, []byte("not an age file"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = fv.Get(context.Background(), "TOKEN")
	wantKind(t, err, cascade.KindIntegrity, "secrets: the file-vault custody store could not be decoded")
	raw, rerr := os.ReadFile(path)
	if rerr != nil || string(raw) != "not an age file" {
		t.Fatalf("vault file was changed: %q, %v", raw, rerr)
	}
}

// TestFileVaultStorePathUnreadableIsUnavailable: a vault path that is a
// directory cannot be read and is unavailable, not an empty vault.
func TestFileVaultStorePathUnreadableIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	fv, err := newFileVaultCustody(Config{Service: "svc", Dir: dir, Passphrase: "linuxreach-pass" + "phrase"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, fileVaultFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = fv.Get(context.Background(), "TOKEN")
	wantKind(t, err, cascade.KindUnavailable, "secrets: the file-vault custody backend is not available on this host")
}

// TestParseVaultEnvMultilineValues: a double-quoted value may span lines and
// its record keeps the line it started on.
func TestParseVaultEnvMultilineValues(t *testing.T) {
	input := "FIRST=plain\nPEM=\"line one\nline two\"\nLAST='a\nb'\nAFTER=x\n"
	entries, err := ParseVaultEnv([]byte(input))
	if err != nil {
		t.Fatalf("ParseVaultEnv: %v", err)
	}
	want := []struct {
		name, value string
		line        int
	}{
		{"FIRST", "plain", 1},
		{"PEM", "line one\nline two", 2},
		{"LAST", "a\nb", 4},
		{"AFTER", "x", 6},
	}
	if len(entries) != len(want) {
		t.Fatalf("parsed %d entries, want %d: %v", len(entries), len(want), names(entries))
	}
	for i, w := range want {
		if entries[i].Name != w.name || string(entries[i].Value) != w.value || entries[i].Line != w.line {
			t.Fatalf("entry %d = %q/%q/line %d, want %q/%q/line %d",
				i, entries[i].Name, entries[i].Value, entries[i].Line, w.name, w.value, w.line)
		}
	}
}

func TestParseVaultEnvUnterminatedMultilineIsRefused(t *testing.T) {
	_, err := ParseVaultEnv([]byte("OK=1\nPEM=\"never closed\nmore text\n"))
	wantKind(t, err, cascade.KindInvalidInput,
		"secrets: vault.env line 2 has an unterminated multi-line quoted value (its content is withheld: it may be a secret)")
}

func TestParseVaultEnvOversizedMultilineIsRefused(t *testing.T) {
	huge := "K=\"" + strings.Repeat("a", maxEnvLineLen/2) + "\n" + strings.Repeat("b", maxEnvLineLen/2+16) + "\n"
	_, err := ParseVaultEnv([]byte(huge))
	wantKind(t, err, cascade.KindInvalidInput,
		"secrets: vault.env line 1 exceeds the maximum assignment length (its content is withheld: it may be a secret)")
}
