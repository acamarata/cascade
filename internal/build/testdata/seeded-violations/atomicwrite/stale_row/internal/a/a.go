package a

import "os"

// Load only reads.
func Load(p string) ([]byte, error) { return os.ReadFile(p) }
