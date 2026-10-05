package a

import "os"

// Save writes state with a bare write.
func Save(p string) error {
	return os.WriteFile(p, nil, 0o600)
}

// Load reads state; ReadFile is not gated.
func Load(p string) ([]byte, error) { return os.ReadFile(p) }
