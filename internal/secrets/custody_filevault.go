// Purpose: the encrypted file-vault custody backend - the universal
//
//	fallback available on every platform, and the only backend on
//	Windows (tier 2).
//
// Inputs: a Config naming the vault directory, an optional passphrase and
//
//	an entropy source.
//
// Outputs: a Custody storing every secret in one age v1 file
//
//	(<dir>/vault.age) whose passphrase is either supplied by the caller or
//	held in a 0600 key file beside it.
//
// Constraints: fails closed. An unreadable or undecryptable vault file is
//
//	an integrity error, never an empty vault: replacing it would destroy
//	every secret it holds. Writes are atomic (temp file + rename) so a
//	crash mid-write cannot truncate the store. No value, passphrase or key
//	byte appears in an error message or a file name.
//
// SPORT: internal/secrets Custody/ADDED (file vault).

package secrets

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	fileVaultName     = "file-vault"
	fileVaultFileName = "vault.age"
	fileVaultKeyName  = "vault.key"
	// fileVaultDirPerm and fileVaultFilePerm keep the vault owner-only.
	fileVaultDirPerm  fs.FileMode = 0o700
	fileVaultFilePerm fs.FileMode = 0o600
)

// fileVaultCustody stores every secret in a single age-encrypted JSON
// object. The whole store is rewritten on each mutation: a vault holds tens
// of entries, not millions, and a whole-file rewrite is what makes the
// atomic-rename durability story simple enough to be true.
type fileVaultCustody struct {
	dir        string
	passphrase string
	rnd        io.Reader
}

// newFileVaultCustody prepares the vault directory and resolves the
// passphrase, creating the 0600 key file on first use when the caller
// supplied none.
func newFileVaultCustody(cfg Config) (*fileVaultCustody, error) {
	if cfg.Dir == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "secrets: the file vault needs a directory")
	}
	if err := os.MkdirAll(cfg.Dir, fileVaultDirPerm); err != nil {
		return nil, ErrCustodyUnavailable(fileVaultName, err)
	}
	fv := &fileVaultCustody{dir: cfg.Dir, passphrase: cfg.Passphrase, rnd: cfg.rand()}
	if fv.passphrase == "" {
		pass, err := fv.loadOrCreateKey()
		if err != nil {
			return nil, err
		}
		fv.passphrase = pass
	}
	return fv, nil
}

// Name reports the backend label used in diagnostics.
func (f *fileVaultCustody) Name() string { return fileVaultName }

// Available reports whether the vault directory is writable. It probes
// rather than assuming: a read-only or missing directory must select no
// backend at all rather than a vault that silently drops writes.
func (f *fileVaultCustody) Available() bool {
	probe := filepath.Join(f.dir, ".cascade-vault-probe")
	if err := os.WriteFile(probe, []byte{}, fileVaultFilePerm); err != nil {
		return false
	}
	return os.Remove(probe) == nil
}

// loadOrCreateKey reads the vault key file, creating it with 32 bytes of
// fresh entropy the first time. The key never leaves this process except as
// the age passphrase.
func (f *fileVaultCustody) loadOrCreateKey() (string, error) {
	path := filepath.Join(f.dir, fileVaultKeyName)
	raw, err := os.ReadFile(path) //nolint:gosec // path is composed from the configured vault dir
	switch {
	case err == nil:
		if len(raw) < 32 {
			return "", ErrCustodyCorrupt(fileVaultName, errors.New("vault key file is too short"))
		}
		return string(raw), nil
	case !errors.Is(err, fs.ErrNotExist):
		return "", ErrCustodyUnavailable(fileVaultName, err)
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(f.rnd, key); err != nil {
		return "", ErrCustodyUnavailable(fileVaultName, err)
	}
	encoded := hex.EncodeToString(key)
	if err := writeFilePrivate(path, []byte(encoded)); err != nil {
		return "", ErrCustodyUnavailable(fileVaultName, err)
	}
	return encoded, nil
}

// load reads and decrypts the store. A missing file is an empty vault; an
// unreadable or undecryptable one is an integrity refusal.
func (f *fileVaultCustody) load() (map[string][]byte, error) {
	path := filepath.Join(f.dir, fileVaultFileName)
	raw, err := os.ReadFile(path) //nolint:gosec // path is composed from the configured vault dir
	if errors.Is(err, fs.ErrNotExist) {
		return map[string][]byte{}, nil
	}
	if err != nil {
		return nil, ErrCustodyUnavailable(fileVaultName, err)
	}
	plain, err := ageDecrypt(f.passphrase, raw)
	if err != nil {
		return nil, ErrCustodyCorrupt(fileVaultName, err)
	}
	entries := map[string][]byte{}
	if err := json.Unmarshal(plain, &entries); err != nil {
		return nil, ErrCustodyCorrupt(fileVaultName, errors.New("vault contents are not a valid entry map"))
	}
	return entries, nil
}

// save encrypts and atomically replaces the store.
func (f *fileVaultCustody) save(entries map[string][]byte) error {
	plain, err := json.Marshal(entries)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "secrets: encoding the file vault failed")
	}
	sealed, err := ageEncrypt(f.passphrase, plain, ageDefaultLogN, f.rnd)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "secrets: encrypting the file vault failed")
	}
	if err := writeFilePrivate(filepath.Join(f.dir, fileVaultFileName), sealed); err != nil {
		return ErrCustodyUnavailable(fileVaultName, err)
	}
	return nil
}

// Set stores value under name, overwriting any existing entry.
func (f *fileVaultCustody) Set(_ context.Context, name string, value []byte) error {
	if err := validateSecretName(name); err != nil {
		return err
	}
	entries, err := f.load()
	if err != nil {
		return err
	}
	entries[name] = append([]byte(nil), value...)
	return f.save(entries)
}

// Get returns the stored value, or ErrSecretNotFound.
func (f *fileVaultCustody) Get(_ context.Context, name string) ([]byte, error) {
	if err := validateSecretName(name); err != nil {
		return nil, err
	}
	entries, err := f.load()
	if err != nil {
		return nil, err
	}
	value, ok := entries[name]
	if !ok {
		return nil, ErrSecretNotFound(name)
	}
	return value, nil
}

// Delete removes name, or reports that it was not stored.
func (f *fileVaultCustody) Delete(_ context.Context, name string) error {
	if err := validateSecretName(name); err != nil {
		return err
	}
	entries, err := f.load()
	if err != nil {
		return err
	}
	if _, ok := entries[name]; !ok {
		return ErrSecretNotFound(name)
	}
	delete(entries, name)
	return f.save(entries)
}

// List returns the stored names, sorted, and never a value.
func (f *fileVaultCustody) List(_ context.Context) ([]string, error) {
	entries, err := f.load()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	return sortedNames(names), nil
}

// writeFilePrivate writes data to path through a temp file in the same
// directory and renames it into place, so a reader never observes a
// half-written vault and a crash never truncates the old one.
func writeFilePrivate(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".vault-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(fileVaultFilePerm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
