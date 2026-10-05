package runtime

// Purpose: contract:atomic-file-write, the one durable file-publish
//   primitive for state under cmd/ and internal/. WriteFileAtomic and
//   WriteReaderAtomic replace a file; CreateFileAtomic publishes one only
//   when nothing is there yet. WriteBytesAtomic (toml_atomic_write.go) is
//   WriteFileAtomic with mode 0600.
// Inputs: a target path, the bytes (or a reader) to publish, and the exact
//   permission bits the published file must carry.
// Outputs: the published file, or a cascade.KindUnavailable error naming
//   the path. An error before the file is published leaves the target as
//   it was with no temp file behind. A directory-sync error after the
//   rename means the new file is already in place but its durability is
//   unconfirmed; CreateFileAtomic can then return (true, err).
// Constraints: one implementation. The target's directory must exist (a
//   missing one is a KindUnavailable error, as os.WriteFile's was; only
//   WriteBytesAtomic creates it). The temp file is created by
//   os.CreateTemp in the target's own directory (mode 0600 at creation, so
//   a reader never sees wider bits than the final ones), then written,
//   synced, chmodded to perm, closed and renamed (or hard-linked) over the
//   target, and the directory is synced afterwards so the rename itself
//   survives a power cut. perm is applied with File.Chmod, so the umask
//   never narrows it. Directory sync is a tested no-op on windows
//   (atomic_write_windows.go); on unix a failing directory sync is an
//   error, never skipped. syncFile and syncDir are package variables only
//   so atomic_write_test.go can count the syncs; production never swaps
//   them.
// SPORT: runtime/atomic-file-write (ADD, P1-CORE-08).

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// atomicTempSuffix ends every temp name this file creates, so a reader of
// the directory can tell a half-published file apart (IsAtomicTempName).
const atomicTempSuffix = ".tmp"

// syncFile flushes a temp file's data before it is published.
var syncFile = func(f *os.File) error { return f.Sync() }

// syncDir flushes a directory's entries after a publish. It is
// syncDirectory from the platform file (fsync on unix, no-op on windows).
var syncDir = syncDirectory

// WriteFileAtomic replaces path with data, published with exactly perm.
// A crash at any point leaves either the old file or the new one, never a
// torn or empty file.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	return WriteReaderAtomic(path, bytes.NewReader(data), perm)
}

// writeOwnerOnlyAtomic is WriteBytesAtomic's body: it creates a missing
// parent directory (0700), then publishes data with mode 0600 through
// WriteFileAtomic. Only WriteBytesAtomic calls it.
func writeOwnerOnlyAtomic(path string, data []byte) error {
	if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
		return atomicWriteErr(path, "create directory", err)
	}
	return WriteFileAtomic(path, data, 0o600)
}

// WriteReaderAtomic replaces path with everything read from r, published
// with exactly perm. A read error from r leaves the target untouched.
func WriteReaderAtomic(path string, r io.Reader, perm os.FileMode) error {
	tmp, err := stageAtomicTemp(path, r, perm)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return atomicWriteErr(path, "publish", err)
	}
	return syncParent(path)
}

// CreateFileAtomic publishes data at path only when path does not exist.
// It links a fully written temp file into place with os.Link, so exactly
// one of several racing callers gets created=true and nobody ever sees a
// partial file. An existing target is left untouched and reported as
// created=false with a nil error. The target directory must support hard
// links (every filesystem cascade supports for its state does).
func CreateFileAtomic(path string, data []byte, perm os.FileMode) (created bool, err error) {
	tmp, err := stageAtomicTemp(path, bytes.NewReader(data), perm)
	if err != nil {
		return false, err
	}
	linkErr := os.Link(tmp, path)
	_ = os.Remove(tmp)
	if linkErr != nil {
		if errors.Is(linkErr, fs.ErrExist) {
			return false, nil
		}
		return false, atomicWriteErr(path, "publish", linkErr)
	}
	if err := restoreLinkedMode(path, perm); err != nil {
		return true, atomicWriteErr(path, "restore mode", err)
	}
	return true, syncParent(path)
}

// IsAtomicTempName reports whether a directory entry name is a temp file
// this package creates while publishing, so a lister can skip one left by
// a crash.
func IsAtomicTempName(name string) bool {
	return strings.HasPrefix(name, ".") && strings.HasSuffix(name, atomicTempSuffix) &&
		strings.Count(name, ".") >= 3
}

// stageAtomicTemp writes r into a new temp file beside path, syncs it and
// sets perm, returning the closed temp file's name. On any failure the
// temp file is removed and the error names path.
func stageAtomicTemp(path string, r io.Reader, perm os.FileMode) (string, error) {
	f, err := os.CreateTemp(dirOf(path), "."+filepath.Base(path)+".*"+atomicTempSuffix)
	if err != nil {
		return "", atomicWriteErr(path, "create temp file", err)
	}
	tmp := f.Name()
	if err := fillAtomicTemp(f, r, perm); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", atomicWriteErr(path, "stage", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", atomicWriteErr(path, "close temp file", err)
	}
	return tmp, nil
}

// fillAtomicTemp copies r into f, syncs the data and applies perm, in the
// order the contract names: write, sync, chmod.
func fillAtomicTemp(f *os.File, r io.Reader, perm os.FileMode) error {
	if _, err := io.Copy(f, r); err != nil {
		return err
	}
	if err := syncFile(f); err != nil {
		return err
	}
	return f.Chmod(perm)
}

// syncParent syncs the directory holding path after a publish.
func syncParent(path string) error {
	if err := syncDir(dirOf(path)); err != nil {
		return atomicWriteErr(path, "sync directory", err)
	}
	return nil
}

// atomicWriteErr is the one error shape of this file: KindUnavailable,
// naming the target path and the step that failed.
func atomicWriteErr(path, step string, err error) error {
	return cascade.Wrapf(cascade.KindUnavailable, err, "runtime: atomic write %s: %s", path, step)
}
