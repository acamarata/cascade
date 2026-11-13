// Package workerregistry provides a registry for background workers with support status tracking.
package workerregistry

import (
	"errors"
	"sync"
)

// ErrBadSupportStatus is returned when a worker status is not one of the allowed values.
var ErrBadSupportStatus = errors.New("bad support status")

// ErrInvalidWorker is returned when a worker name is empty or the registry receiver is nil.
var ErrInvalidWorker = errors.New("invalid worker")

// ErrDuplicateWorker is returned when a worker with the same name is already registered.
var ErrDuplicateWorker = errors.New("duplicate worker")

// Worker represents a background worker with its support status.
type Worker struct {
	Name         string
	Experimental bool
	Status       string
}

// Registry maintains a collection of workers.
// The zero value for Registry is an empty registry ready for use.
type Registry struct {
	mu      sync.Mutex
	entries []Worker
}

// NewRegistry returns a new, empty Registry.
func NewRegistry() *Registry {
	return &Registry{}
}

// Register adds a worker to the registry after validating it.
// Validation order: name, then status/defaults, then duplicate.
// If the receiver is nil, it returns ErrInvalidWorker.
func (r *Registry) Register(w Worker) error {
	if r == nil {
		return ErrInvalidWorker
	}
	if w.Name == "" {
		return ErrInvalidWorker
	}
	if w.Status != "" {
		if w.Status != "supported" && w.Status != "experimental" && w.Status != "deprecated" {
			return ErrBadSupportStatus
		}
	} else {
		if w.Experimental {
			w.Status = "experimental"
		} else {
			w.Status = "supported"
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, existing := range r.entries {
		if existing.Name == w.Name {
			return ErrDuplicateWorker
		}
	}

	r.entries = append(r.entries, w)
	return nil
}

// List returns a copy of all registered workers.
func (r *Registry) List() []Worker {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Worker, len(r.entries))
	copy(out, r.entries)
	return out
}
