package a

import "os"

// Save captures the bare write as a function value.
func Save(p string) error {
	w := os.WriteFile
	return w(p, nil, 0o600)
}
