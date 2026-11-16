// Package eligibility provides the surface-eligibility contract gate.
package eligibility

import (
	"errors"
	"sync"
)

// ErrLooseningWithoutElevation is returned when Set attempts to loosen
// permissions without the required elevation context.
var ErrLooseningWithoutElevation = errors.New("eligibility: cannot loosen permissions without elevation")

// memoryRegistry is an in-memory implementation of Registry that enforces
// tighten-only semantics at runtime.
type memoryRegistry struct {
	mu   sync.RWMutex
	rows map[string]Row
}

// NewMemoryRegistry returns a Registry backed by an in-memory store.
func NewMemoryRegistry() Registry {
	return &memoryRegistry{
		rows: make(map[string]Row),
	}
}

// Get returns the Row for the given operationID.
// Unregistered operations receive a default-deny row with no error.
func (r *memoryRegistry) Get(operationID string) (Row, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if row, ok := r.rows[operationID]; ok {
		return row, nil
	}

	// Default deny: all permission flags false.
	return Row{
		OperationID:            operationID,
		PermittedBrowser:       false,
		ConsumerChatAutomation: false,
		ProviderAuthorised:     false,
	}, nil
}

// Set stores the given row. It rejects any change that loosens permissions
// relative to the existing row unless elevation is provided (not yet modelled).
// Tightening (setting a true flag to false) is always allowed.
func (r *memoryRegistry) Set(row Row) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, exists := r.rows[row.OperationID]
	if exists && isLoosening(existing, row) {
		return ErrLooseningWithoutElevation
	}

	r.rows[row.OperationID] = row
	return nil
}

// IsExpired returns true when the clock's time is strictly after the row's Expiry time.
// An expired row should be treated as needs_review and the operation refused.
func (r *memoryRegistry) IsExpired(row Row, c Clock) bool {
	if row.Expiry.IsZero() {
		return false
	}
	return c.Now().After(row.Expiry)
}

// isLoosening reports whether next loosens any permission relative to prev.
// A permission is loosened when it changes from false to true.
func isLoosening(prev, next Row) bool {
	if !prev.PermittedBrowser && next.PermittedBrowser {
		return true
	}
	if !prev.ConsumerChatAutomation && next.ConsumerChatAutomation {
		return true
	}
	if !prev.ProviderAuthorised && next.ProviderAuthorised {
		return true
	}
	return false
}
