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

// WriteAtomic writes data to path via a temporary file in path's OWN
// directory followed by a rename, the same primitive internal/runtime's
// writeBytesAtomic established for the config tree and for the reason its
// comment records: the temp file must share a directory with the target,
// because a rename across volumes is not atomic anywhere and on Windows
// may be refused outright. Deriving the directory with filepath.Dir rather
// than by searching for '/' is part of that same fix, which is why this
// implementation matches rather than invents.
//
// Rename replaces an existing file on every platform this repo targets
// (POSIX rename, and MoveFileEx with replace-existing on Windows, which is
// what the Go runtime issues). It is that same-directory replacement that
// is atomic; a rename in general is not, which is why the temp file is
// never placed in the system temp directory.
//
// The data is flushed to stable storage before the rename, so a crash
// cannot leave the rename durable while the bytes it published are not.
// The containing directory is deliberately not also synced: opening a
// directory for sync is not portable to Windows, and an honest comment is
// better than a platform-split for the last increment of durability.
func (osFS) WriteAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".memory-*.md.tmp")
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

// writeAndSync writes data to f, flushes it to stable storage, and closes
// it, reporting the first failure. Split out so WriteAtomic stays inside
// the 50-line limit.
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

// isNotExist reports whether err means "no such file". It tests the
// portable fs.ErrNotExist sentinel rather than calling os.IsNotExist, so
// it is equally correct for the real file system and for a substituted one
// in a test, which must return the same sentinel to be a faithful double.
func isNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}
