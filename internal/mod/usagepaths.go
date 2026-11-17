package mod

import (
	"errors"
	"sync"
)

// ErrUnknownModule is returned when a module is not registered in the registry.
var ErrUnknownModule = errors.New("unknown module")

// ErrDuplicateModule is returned when a module is registered more than once.
var ErrDuplicateModule = errors.New("duplicate module")

// Registry stores per-module usage-cache.json path lists.
type Registry struct {
	mu      sync.RWMutex
	modules map[string][]string
}

// NewRegistry creates a new empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		modules: make(map[string][]string),
	}
}

// Register stores a module's own usage-cache.json path list.
// It returns ErrDuplicateModule if the module is already registered.
func (r *Registry) Register(module string, paths []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.modules[module]; exists {
		return ErrDuplicateModule
	}
	copiedPaths := make([]string, len(paths))
	copy(copiedPaths, paths)
	r.modules[module] = copiedPaths
	return nil
}

// PathsFor returns only the paths registered for the given module.
// It returns ErrUnknownModule if the module is not registered.
func (r *Registry) PathsFor(module string) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	paths, exists := r.modules[module]
	if !exists {
		return nil, ErrUnknownModule
	}
	copiedPaths := make([]string, len(paths))
	copy(copiedPaths, paths)
	return copiedPaths, nil
}

// Resolve reports whether the given path belongs to the specified module.
// It returns false and ErrUnknownModule if the module is not registered.
func (r *Registry) Resolve(module, path string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	paths, exists := r.modules[module]
	if !exists {
		return false, ErrUnknownModule
	}
	for _, p := range paths {
		if p == path {
			return true, nil
		}
	}
	return false, nil
}
