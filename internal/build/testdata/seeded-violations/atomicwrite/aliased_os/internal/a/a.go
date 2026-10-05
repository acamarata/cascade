package a

import stdos "os"

// Save hides the bare write behind an alias.
func Save(p string) error {
	return stdos.WriteFile(p, nil, 0o600)
}
