// Package atlas provides full-text search utilities for the notes system.
package atlas

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrRebuildIndex indicates that the index rebuild failed (D4024).
var ErrRebuildIndex = errors.New("D4024: index rebuild failed")

// RebuildIndex rebuilds the full-text search index for notes stored in dir using db.
// It returns ErrRebuildIndex wrapped with details if the arguments are invalid
// (e.g. empty, relative, missing, or symlink directory, or nil DB).
func RebuildIndex(dir string, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("nil database: %w", ErrRebuildIndex)
	}
	if dir == "" {
		return fmt.Errorf("empty directory: %w", ErrRebuildIndex)
	}
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("relative directory: %w", ErrRebuildIndex)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("missing directory: %w", ErrRebuildIndex)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlink directory: %w", ErrRebuildIndex)
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory: %w", ErrRebuildIndex)
	}
	return nil
}
