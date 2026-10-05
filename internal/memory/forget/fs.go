package forget

// Purpose: the crash-safe file primitives the forget account is written
//   with. The account is the only durable record of an in-flight
//   retirement, so a half-written one would turn a resumable interruption
//   into an unexplained absence.
// Inputs: a path and the bytes to publish there.
// Outputs: the file, replaced atomically, or the raw error for the caller
//   to classify.
// Constraints: writes go through runtime.WriteFileAtomic: the temporary
//   file shares the target's directory, because a rename across volumes is
//   not atomic anywhere and on Windows may be refused outright; the bytes
//   are flushed before the rename and the directory after it.
// SPORT: internal/memory/forget (ADD, P1-E07-W2-S14-T4).

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/internal/runtime"
)

// dirPerm and filePerm match the memory store's own modes: a private tree
// under the user's home, readable and writable by its owner alone.
const (
	dirPerm  fs.FileMode = 0o700
	filePerm fs.FileMode = 0o600
)

// writeAtomic publishes data at path, replacing whatever is there, through
// runtime.WriteFileAtomic (the one atomic-write implementation). The
// parent directory is created with dirPerm first.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return err
	}
	return runtime.WriteFileAtomic(path, data, filePerm)
}

// readFile reads a whole file, returning the raw error for the caller to
// classify. It is a named seam rather than a direct os.ReadFile call so
// every read in this package goes through one place.
func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

// isNotExist reports whether err means "no such file". It tests the
// portable fs.ErrNotExist sentinel rather than calling os.IsNotExist, so
// it is equally correct for a wrapped error.
func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
