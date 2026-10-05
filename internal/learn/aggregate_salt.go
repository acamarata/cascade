package learn

// Purpose: the aggregate salt file (R-21.167). EnsureAggregateSalt creates
//   the 32-byte salt exactly once and loadSalt validates it before every
//   use; hashRepoID is the only form a repo id takes outside the database.
// Inputs: a salt file path; crypto/rand for the one-time salt.
// Outputs: the validated 32 salt bytes, or a typed refusal.
// Constraints: creation writes a 0600 temp file in the salt directory, syncs
//   it, then os.Link's it to the final path, so the file at path is complete
//   the instant it exists; a concurrent or crashed creator can never leave a
//   0-byte salt, and a loser of the race (EEXIST) reads the winner's file.
//   Refusals: absent -> KindNotFound; a symlink, a non-regular file or a size
//   other than 32 -> KindIntegrity; a group/world accessible file or salt
//   directory -> KindPermissionDenied. The directory is created 0700. The
//   mode checks are skipped on windows (mode bits carry no ACL). An invalid
//   existing salt is never overwritten.
// SPORT: internal.learn.EnsureAggregateSalt/ADDED (P1-CAP-03).

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	goruntime "runtime"

	"github.com/zeebo/blake3"

	"github.com/acamarata/cascade/pkg/cascade"
)

const saltLen = 32 // aggregate salt size in bytes

// EnsureAggregateSalt returns the salt stored at path, creating it exactly
// once: 32 random bytes, mode 0600, parent directory 0700. An existing valid
// file is returned unchanged; an existing invalid one is refused and never
// overwritten.
func EnsureAggregateSalt(path string) ([]byte, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: create the salt directory")
	}
	if err := checkSaltDir(dir); err != nil {
		return nil, err
	}
	tmp, err := writeSaltTemp(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(tmp) }()
	if err := os.Link(tmp, path); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: publish the aggregate salt file")
	}
	return loadSalt(path)
}

// writeSaltTemp writes a fresh random salt to a 0600 temp file in dir and
// returns its path; the caller removes it.
func writeSaltTemp(dir string) (string, error) {
	f, err := os.CreateTemp(dir, ".salt-*") // 0600
	if err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "learn: create the aggregate salt temp file")
	}
	salt := make([]byte, saltLen)
	_, err = io.ReadFull(rand.Reader, salt)
	if err == nil {
		_, err = f.Write(salt)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", cascade.Wrap(cascade.KindUnavailable, err, "learn: write the aggregate salt temp file")
	}
	return f.Name(), nil
}

// checkSaltDir refuses a salt directory that is not a directory or is
// group/world accessible. A missing directory is an absent salt. The
// directory is Stat'd, not Lstat'd: a home directory moved off the boot disk
// and symlinked back is legitimate, and its target's mode is what counts.
func checkSaltDir(dir string) error {
	info, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return cascade.New(cascade.KindNotFound, "learn: aggregate salt file is absent; aggregation refused")
	}
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "learn: stat the aggregate salt directory")
	}
	if !info.IsDir() {
		return cascade.New(cascade.KindIntegrity, "learn: aggregate salt directory is not a directory; aggregation refused")
	}
	if goruntime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return cascade.New(cascade.KindPermissionDenied, "learn: aggregate salt directory is group or world accessible; aggregation refused")
	}
	return nil
}

// loadSalt reads and validates the salt file; it never creates one. The path
// is Lstat'd (a symlink is refused), opened, and the opened file must be the
// same file and pass the size and mode checks, so a swap between the check
// and the read is caught.
func loadSalt(path string) ([]byte, error) {
	if err := checkSaltDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	lst, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, cascade.New(cascade.KindNotFound, "learn: aggregate salt file is absent; aggregation refused")
	}
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: stat the aggregate salt file")
	}
	if !lst.Mode().IsRegular() {
		return nil, cascade.New(cascade.KindIntegrity, "learn: aggregate salt file is not a regular file (a symlink is refused); aggregation refused")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: open the aggregate salt file")
	}
	defer func() { _ = f.Close() }()
	return readOpenedSalt(f, lst)
}

// readOpenedSalt validates the opened file against the Lstat result and reads
// the salt.
func readOpenedSalt(f *os.File, lst fs.FileInfo) ([]byte, error) {
	info, err := f.Stat()
	if err != nil || !os.SameFile(lst, info) || !info.Mode().IsRegular() || info.Size() != saltLen {
		return nil, cascade.New(cascade.KindIntegrity, "learn: aggregate salt file is not a regular file of exactly 32 bytes; aggregation refused")
	}
	if goruntime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, cascade.New(cascade.KindPermissionDenied, "learn: aggregate salt file is group or world accessible; aggregation refused")
	}
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(f, salt); err != nil {
		return nil, cascade.New(cascade.KindIntegrity, "learn: aggregate salt file is unreadable or not exactly 32 bytes; aggregation refused")
	}
	return salt, nil
}

// hashRepoID is hex(blake3(salt || repoID)), a repo id's only outbound form.
func hashRepoID(salt []byte, repoID string) string {
	sum := blake3.Sum256(append(append([]byte{}, salt...), repoID...))
	return hex.EncodeToString(sum[:])
}
