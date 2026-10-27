// Purpose: the file vault's durable file writes: a synced 0600 temp file
//
//	renamed into place (the store), or linked into place only if absent (the
//	vault key), with the parent directory synced after either.
//
// SPORT: internal/secrets file-vault writes (split for the 300-line cap, P1-SEC-23).

package secrets

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// linkFile is os.Link; tests replace it to reach the O_EXCL fallback.
var linkFile = os.Link

// fileWrite is (*os.File).Write; tests replace it to fail the fallback write.
var fileWrite = (*os.File).Write

// loadExistingKey reads a key file that must already exist.
func (f *fileVaultCustody) loadExistingKey(path string) (string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path is composed from the configured vault dir
	if err != nil {
		return "", ErrCustodyUnavailable(fileVaultName, err)
	}
	if len(raw) < 32 {
		return "", ErrCustodyCorrupt(fileVaultName, errors.New("vault key file is too short"))
	}
	return string(raw), nil
}

// createFilePrivate writes data to a 0600 temp file, syncs it, and links it
// into place only if path does not exist (fs.ErrExist otherwise), so a
// complete file appears atomically and an existing one is never replaced.
// A filesystem without hard links gets an O_EXCL create instead.
func createFilePrivate(path string, data []byte) error {
	tmp, err := writeTempPrivate(filepath.Dir(path), data)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp) }()
	if err := linkFile(tmp, path); err == nil || errors.Is(err, fs.ErrExist) {
		if err == nil {
			syncDir(filepath.Dir(path))
		}
		return err
	}
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileVaultFilePerm) //nolint:gosec // composed from the vault dir
	if err != nil {
		return err
	}
	// A failed write or sync removes the partial file: a short key file
	// would otherwise be read as corrupt by every later opener.
	if _, err := fileWrite(out, data); err != nil {
		_ = out.Close()
		_ = os.Remove(path)
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(path)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}

// writeFilePrivate writes data to path through a temp file in the same
// directory and renames it into place, so a reader never observes a
// half-written vault and a crash never truncates the old one.
func writeFilePrivate(path string, data []byte) error {
	tmpName, err := writeTempPrivate(filepath.Dir(path), data)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmpName) }()
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}

// writeTempPrivate writes data to a synced 0600 temp file in dir and
// returns its name.
func writeTempPrivate(dir string, data []byte) (string, error) {
	tmp, err := os.CreateTemp(dir, ".vault-*.tmp")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	fail := func(err error) (string, error) { _ = tmp.Close(); _ = os.Remove(name); return "", err }
	if err := tmp.Chmod(fileVaultFilePerm); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

// syncDir flushes a directory entry change (a rename or link) to disk. A
// platform that cannot sync a directory (Windows) is left to its own
// durability; the error is not a write failure.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil { //nolint:gosec // the vault dir
		_ = d.Sync()
		_ = d.Close()
	}
}
