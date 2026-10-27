// Purpose: the file vault's exclusive key creation: an existing file is
//
//	never replaced (by link or by the O_EXCL fallback), and a short or
//	missing key file is refused.
//
// SPORT: internal/secrets file-vault write tests (P1-SEC-23).

package secrets

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestCreateFilePrivateNeverReplaces(t *testing.T) {
	for name, link := range map[string]func(string, string) error{
		"link":     os.Link,
		"fallback": func(string, string) error { return errors.New("fake: no hard links here") },
	} {
		previous := linkFile
		linkFile = link
		dir := t.TempDir()
		path := filepath.Join(dir, fileVaultKeyName)
		if err := createFilePrivate(path, []byte("fake-first")); err != nil {
			linkFile = previous
			t.Fatalf("%s: first create: %v", name, err)
		}
		err := createFilePrivate(path, []byte("fake-second"))
		linkFile = previous
		got, _ := os.ReadFile(path) //nolint:gosec // test temp dir
		if !errors.Is(err, fs.ErrExist) || string(got) != "fake-first" {
			t.Fatalf("%s: second create = %v, file %q; want ErrExist and the first key kept", name, err, got)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 1 {
			t.Fatalf("%s: temp files left behind: %v", name, entries)
		}
	}
}

func TestLoadExistingKeyRefusesShortOrMissing(t *testing.T) {
	dir := t.TempDir()
	f := &fileVaultCustody{dir: dir}
	path := filepath.Join(dir, fileVaultKeyName)
	if _, err := f.loadExistingKey(path); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("missing key file = %v, want unavailable", err)
	}
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.loadExistingKey(path); err == nil {
		t.Fatal("a short key file was accepted")
	}
	if _, err := writeTempPrivate(filepath.Join(dir, "missing-dir"), []byte("x")); err == nil {
		t.Fatal("a temp write into a missing directory succeeded")
	}
}

func TestAcquireLockFileRefusesAnUnusablePath(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireLockFile(t.Context(), filepath.Join(blocker, "sub", "x.lock")); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("a lock under a regular file = %v, want unavailable", err)
	}
	if _, err := newFileVaultCustody(Config{Service: DefaultVaultService, Dir: blocker}); err == nil {
		t.Fatal("a vault over a regular file opened")
	}
}

func TestCreateFilePrivateFallbackLeavesNoPartialFile(t *testing.T) {
	prevLink, prevWrite := linkFile, fileWrite
	linkFile = func(string, string) error { return errors.New("fake: no hard links here") }
	fileWrite = func(f *os.File, b []byte) (int, error) {
		n, _ := f.Write(b[:len(b)/2])
		return n, errors.New("fake: disk full")
	}
	t.Cleanup(func() { linkFile, fileWrite = prevLink, prevWrite })
	dir := t.TempDir()
	path := filepath.Join(dir, fileVaultKeyName)
	if err := createFilePrivate(path, []byte("fake-key-material-0123456789abcdef")); err == nil {
		t.Fatal("a failed fallback write reported success")
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a failed write left a partial key file (stat err %v)", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("files left behind: %v", entries)
	}
}
