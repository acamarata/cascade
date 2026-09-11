package nodes

import (
	"os"
	"path/filepath"
)

// writeGarbage writes a not-valid-JSON file at dir/name, creating dir if
// needed. Shared by records_test.go and knownhosts_test.go to exercise
// each file backend's corrupt-store refusal path.
func writeGarbage(dir, name string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), []byte("not valid json {{{"), 0o600)
}
