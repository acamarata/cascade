// Package crunch provides cooperative locking for migration callers.
package crunch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrTreeLocked is returned when a path is within a locked tree.
var ErrTreeLocked = errors.New("crunch: tree locked")

// ErrPathNotFound is returned when a path does not exist.
var ErrPathNotFound = errors.New("crunch: path not found")

// ErrInvalidTreePath is returned when a path is invalid, empty, or not a directory.
var ErrInvalidTreePath = errors.New("crunch: invalid tree path")

var (
	mu     sync.RWMutex
	locked = make(map[string]bool)
)

// LockTrees locks the given directory paths.
func LockTrees(paths ...string) error {
	cleaned := make([]string, 0, len(paths))
	seen := make(map[string]bool)

	for _, p := range paths {
		if p == "" {
			return ErrInvalidTreePath
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		c := filepath.Clean(abs)
		if seen[c] {
			return ErrInvalidTreePath
		}
		seen[c] = true
		info, err := os.Stat(c)
		if err != nil {
			if os.IsNotExist(err) {
				return ErrPathNotFound
			}
			return err
		}
		if !info.IsDir() {
			return ErrInvalidTreePath
		}
		cleaned = append(cleaned, c)
	}

	mu.Lock()
	defer mu.Unlock()

	for _, c := range cleaned {
		locked[c] = true
	}
	return nil
}

// UnlockTrees unlocks the given directory paths.
func UnlockTrees(paths ...string) error {
	cleaned := make([]string, 0, len(paths))
	seen := make(map[string]bool)

	for _, p := range paths {
		if p == "" {
			return ErrInvalidTreePath
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		c := filepath.Clean(abs)
		if seen[c] {
			return ErrInvalidTreePath
		}
		seen[c] = true
		cleaned = append(cleaned, c)
	}

	mu.Lock()
	defer mu.Unlock()

	for _, c := range cleaned {
		delete(locked, c)
	}
	return nil
}

// CheckWritable returns ErrTreeLocked if the path is within a locked tree.
func CheckWritable(path string) error {
	if path == "" {
		return ErrInvalidTreePath
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	c := filepath.Clean(abs)

	mu.RLock()
	defer mu.RUnlock()

	for root := range locked {
		rel, err := filepath.Rel(root, c)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(rel, "..") {
			return ErrTreeLocked
		}
	}
	return nil
}
