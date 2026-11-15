package log

import (
	"os"
	"path/filepath"
)

// RunWithScratch creates a temporary scratch directory under base, runs fn with
// the path to that directory, then removes the directory regardless of whether
// fn returns an error or panics. If fn panics, the panic is re-raised after
// cleanup. Returns the error from fn, or nil if fn succeeded.
func RunWithScratch(base string, fn func(string) error) (err error) {
	dir, mkErr := os.MkdirTemp(base, "scratch-")
	if mkErr != nil {
		return mkErr
	}
	defer func() {
		_ = os.RemoveAll(dir)
	}()

	defer func() {
		if r := recover(); r != nil {
			_ = os.RemoveAll(dir)
			panic(r)
		}
	}()

	return fn(dir)
}

// ScratchDir creates a new scratch directory under base and returns its path.
// The caller is responsible for removing it.
func ScratchDir(base string) (string, error) {
	return os.MkdirTemp(base, "scratch-")
}

// EnsureDir creates dir if it does not already exist.
func EnsureDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

// JoinPaths joins elem... to base using filepath.Join.
func JoinPaths(base string, elem ...string) string {
	return filepath.Join(append([]string{base}, elem...)...)
}
