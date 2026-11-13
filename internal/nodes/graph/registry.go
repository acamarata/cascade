package graph

import (
	"crypto/sha256"
	"errors"
	"fmt"
)

// Registry tracks nodes by name and key fingerprint.
type Registry struct {
	entries map[string]string
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		entries: make(map[string]string),
	}
}

// Register adds a node identified by name and key. The identity is the
// pair of name plus the SHA-256 hex fingerprint of key. Registering the
// same name and key again returns the existing id. A different key for
// the same name produces a distinct id. Empty name or empty key fails.
func (r *Registry) Register(name string, key []byte) (string, error) {
	if name == "" {
		return "", errors.New("graph: empty name")
	}
	if len(key) == 0 {
		return "", errors.New("graph: empty key")
	}

	fp := fmt.Sprintf("%x", sha256.Sum256(key))
	composite := name + "\x00" + fp

	if id, ok := r.entries[composite]; ok {
		return id, nil
	}

	id := fmt.Sprintf("%s-%s", name, fp[:16])
	r.entries[composite] = id
	return id, nil
}

// Len returns the number of registered entries.
func (r *Registry) Len() int {
	return len(r.entries)
}
