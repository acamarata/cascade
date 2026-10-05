package a

import . "os"

// Save calls the bare write through a dot-import.
func Save(p string) error {
	return WriteFile(p, nil, 0o600)
}
