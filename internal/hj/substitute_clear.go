package hj

import (
	"fmt"
	"os"
	"path/filepath"
)

// ClearSubstitute removes all files and subdirectories within the given
// directory but leaves the directory itself intact. The total size of the
// remaining folder is exactly 0 bytes. It returns an error if the path is
// missing or is not a directory.
func ClearSubstitute(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("stat %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read dir %s: %w", dir, err)
	}

	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return fmt.Errorf("remove %s: %w", entry.Name(), err)
		}
	}
	return nil
}
