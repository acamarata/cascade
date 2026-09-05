package forget

// Purpose: the crash-safe file primitives the forget account is written
//   with. The account is the only durable record of an in-flight
//   retirement, so a half-written one would turn a resumable interruption
//   into an unexplained absence.
// Inputs: a path and the bytes to publish there.
// Outputs: the file, replaced atomically, or the raw error for the caller
//   to classify.
// Constraints: the temporary file shares the target's directory, because a
//   rename across volumes is not atomic anywhere and on Windows may be
//   refused outright; the bytes are flushed before the rename, so a crash
//   cannot leave the rename durable while the data it published is not.
// SPORT: internal/memory/forget (ADD, P1-E07-W2-S14-T4).

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// dirPerm and filePerm match the memory store's own modes: a private tree
// under the user's home, readable and writable by its owner alone.
const (
	dirPerm  fs.FileMode = 0o700
	filePerm fs.FileMode = 0o600
)

// writeAtomic publishes data at path, replacing whatever is there.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".forget-*.json.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := writeAndSync(tmp, data); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, filePerm); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// writeAndSync writes data to f, flushes it to stable storage and closes
// it, reporting the first failure. Split out so writeAtomic stays inside
// the 50-line function cap.
func writeAndSync(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// readFile reads a whole file, returning the raw error for the caller to
// classify. It is a named seam rather than a direct os.ReadFile call so
// every read in this package goes through one place.
func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

// isNotExist reports whether err means "no such file". It tests the
// portable fs.ErrNotExist sentinel rather than calling os.IsNotExist, so
// it is equally correct for a wrapped error.
func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
