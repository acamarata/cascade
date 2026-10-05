package a

import "os"

// Save writes state with a bare write.
func Save(p string) error {
	return os.WriteFile(p, nil, 0o600)
}
