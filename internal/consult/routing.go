// Package consult provides routing preference storage for context sessions.
package consult

import (
	"errors"
	"strings"
)

// ErrNoRoutingPreference is returned when a context has no saved preference.
var ErrNoRoutingPreference = errors.New("no routing preference")

// ErrInvalidRoutingPreference is returned when input validation fails.
var ErrInvalidRoutingPreference = errors.New("invalid routing preference")

// ErrInvalidContextMemory is returned when the receiver is nil.
var ErrInvalidContextMemory = errors.New("invalid context memory")

// RoutingPreference holds routing configuration for a context.
type RoutingPreference struct {
	Class string
	Model string
}

// ContextMemory stores routing preferences keyed by context ID.
type ContextMemory struct {
	preferences map[string]RoutingPreference
}

// NewContextMemory creates an initialized ContextMemory.
func NewContextMemory() *ContextMemory {
	return &ContextMemory{
		preferences: make(map[string]RoutingPreference),
	}
}

// SaveRoutingPreference stores a routing preference for the given context ID.
// It initializes the internal map if the ContextMemory is zero-value.
// Blank ctxID, Class, or Model (after trimming spaces) returns ErrInvalidRoutingPreference
// without mutating state. A nil receiver returns ErrInvalidContextMemory.
// Saving an existing ctxID replaces its preference atomically.
func (m *ContextMemory) SaveRoutingPreference(ctxID string, p RoutingPreference) error {
	if m == nil {
		return ErrInvalidContextMemory
	}
	if strings.TrimSpace(ctxID) == "" || strings.TrimSpace(p.Class) == "" || strings.TrimSpace(p.Model) == "" {
		return ErrInvalidRoutingPreference
	}
	if m.preferences == nil {
		m.preferences = make(map[string]RoutingPreference)
	}
	m.preferences[ctxID] = p
	return nil
}

// LoadRoutingPreference retrieves the routing preference for the given context ID.
// A blank ctxID (after trimming spaces) returns zero preference and ErrInvalidRoutingPreference.
// A missing ctxID returns zero preference and ErrNoRoutingPreference.
// A nil receiver returns zero preference and ErrInvalidContextMemory.
func (m *ContextMemory) LoadRoutingPreference(ctxID string) (RoutingPreference, error) {
	if m == nil {
		return RoutingPreference{}, ErrInvalidContextMemory
	}
	if strings.TrimSpace(ctxID) == "" {
		return RoutingPreference{}, ErrInvalidRoutingPreference
	}
	if m.preferences == nil {
		return RoutingPreference{}, ErrNoRoutingPreference
	}
	p, ok := m.preferences[ctxID]
	if !ok {
		return RoutingPreference{}, ErrNoRoutingPreference
	}
	return p, nil
}
