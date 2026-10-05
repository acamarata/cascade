package memory

// Purpose: the file-system seam FileStore performs every read and write
//   through, and the real OS implementation of it, including the
//   crash-safe atomic write. Split from file_store.go per the 300-line
//   file cap.
// Inputs: paths and bytes.
// Outputs: bytes, directory listings, or the underlying OS error, which
//   the caller classifies; nothing here constructs a taxonomy error.
// Constraints: the seam is unexported and the only implementation reachable
//   from a shipped path is the real one (Art.1). Tests substitute a
//   failing implementation declared in _test.go.
// SPORT: G/memory-store (ADD, placeholder per T-1 sport_updates).

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/internal/runtime"
)

// dirPerm and filePerm are the permissions the store creates with. Memory
// records are the user's own notes and are owner-only, matching the
// posture internal/runtime already uses for the config tree.
const (
	dirPerm  fs.FileMode = 0o700
	filePerm fs.FileMode = 0o600
)

// fileSystem is the seam every FileStore operation goes through. It is
// unexported on purpose: it exists so a test can inject a file system that
// fails, and an exported seam would be an invitation to ship an
// alternative implementation, which Art.1 forbids and which nothing needs.
type fileSystem interface {
	// ReadFile returns the contents of the file at path.
	ReadFile(path string) ([]byte, error)
	// WriteAtomic writes data to path so that a crash or a failure part
	// way through leaves the previous contents intact rather than a
	// truncated file.
	WriteAtomic(path string, data []byte) error
	// Remove deletes the file at path.
	Remove(path string) error
	// Exists reports whether a regular file exists at path.
	Exists(path string) (bool, error)
	// ReadDirNames returns the entry names in dir. A missing directory is
	// an empty listing, not an error: a kind with no records yet is a
	// normal state, not a fault.
	ReadDirNames(dir string) ([]string, error)
}

// osFS is the production implementation, backed by the real file system.
// It is the only implementation any shipped path uses.
type osFS struct{}

// ReadFile reads path.
func (osFS) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

// Remove deletes path.
func (osFS) Remove(path string) error { return os.Remove(path) }

// Exists reports whether a regular file exists at path.
func (osFS) Exists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return info.Mode().IsRegular(), nil
}

// ReadDirNames lists dir, treating a missing directory as empty.
func (osFS) ReadDirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		names = append(names, e.Name())
	}
	return names, nil
}

// WriteAtomic writes data to path through runtime.WriteFileAtomic, the one
// atomic-write implementation (contract:atomic-file-write): a temp file in
// path's own directory, flushed, given filePerm, renamed over the target,
// then the directory synced. A crash leaves the old file or the new one,
// never a torn one. The parent directory is created with the store's own
// dirPerm first, so the helper never picks the directory mode.
func (osFS) WriteAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return err
	}
	return runtime.WriteFileAtomic(path, data, filePerm)
}

// isNotExist reports whether err means "no such file". It tests the
// portable fs.ErrNotExist sentinel rather than calling os.IsNotExist, so
// it is equally correct for the real file system and for a substituted one
// in a test, which must return the same sentinel to be a faithful double.
func isNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}
