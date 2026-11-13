// Package write provides configuration file operations.
package write

import (
	"errors"
	"fmt"
	"os"

	"github.com/acamarata/cascade/internal/runtime"
)

// Set writes data to the file at path. If an existing file is present,
// it is backed up as path + ".bak" before writing. If the file does not
// exist, no backup is created. On read errors other than non-existence,
// Set fails closed and returns an error without writing anything.
func Set(path string, data []byte) error {
	old, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("reading current config: %w", err)
		}
	} else {
		bakPath := path + ".bak"
		if writeErr := runtime.WriteFileAtomic(bakPath, old, 0644); writeErr != nil {
			return fmt.Errorf("writing backup: %w", writeErr)
		}
	}

	if writeErr := runtime.WriteFileAtomic(path, data, 0644); writeErr != nil {
		return fmt.Errorf("writing new config: %w", writeErr)
	}

	return nil
}
