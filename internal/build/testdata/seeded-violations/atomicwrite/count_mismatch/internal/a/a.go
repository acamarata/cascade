package a

import "os"

// Save writes twice inside a function whose row allows one.
func Save(p string) error {
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		return err
	}
	return os.WriteFile(p+".bak", nil, 0o600)
}
