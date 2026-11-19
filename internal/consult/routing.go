// Package consult provides routing preference storage for context sessions.
package consult

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
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

// marshalRecord is the JSON shape for a single routing preference entry.
type marshalRecord struct {
	ContextID string `json:"context_id"`
	Class     string `json:"class"`
	Model     string `json:"model"`
}

// Marshal serializes the ContextMemory into a JSON array sorted lexically by context ID.
// Each record contains exactly the keys "context_id", "class", "model" in that order.
// An empty state emits [] with no trailing newline.
// A nil receiver returns nil data and ErrInvalidContextMemory.
func (m *ContextMemory) Marshal() ([]byte, error) {
	if m == nil {
		return nil, ErrInvalidContextMemory
	}
	keys := make([]string, 0, len(m.preferences))
	for k := range m.preferences {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	records := make([]marshalRecord, 0, len(keys))
	for _, k := range keys {
		p := m.preferences[k]
		records = append(records, marshalRecord{
			ContextID: k,
			Class:     p.Class,
			Model:     p.Model,
		})
	}
	data, err := json.Marshal(records)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// UnmarshalContextMemory deserializes a JSON byte slice into a new ContextMemory.
// It requires an array of objects each with exactly the keys "context_id", "class", "model".
// Input key order and record order may vary. Rejects nil/empty bytes, null, malformed JSON,
// trailing data, missing/unknown/duplicate keys, duplicate context_id, and blank values,
// returning nil and ErrInvalidContextMemory in all such cases.
// A successful result owns an independent map.
func UnmarshalContextMemory(data []byte) (*ContextMemory, error) {
	if len(data) == 0 {
		return nil, ErrInvalidContextMemory
	}

	dec := json.NewDecoder(bytes.NewReader(data))

	// Read opening '['
	t, err := dec.Token()
	if err != nil {
		return nil, ErrInvalidContextMemory
	}
	if delim, ok := t.(json.Delim); !ok || delim != '[' {
		return nil, ErrInvalidContextMemory
	}

	m := NewContextMemory()
	seen := make(map[string]bool)

	for dec.More() {
		// Read opening '{'
		t, err := dec.Token()
		if err != nil {
			return nil, ErrInvalidContextMemory
		}
		if delim, ok := t.(json.Delim); !ok || delim != '{' {
			return nil, ErrInvalidContextMemory
		}

		var ctxID, class, model string
		seenKeys := make(map[string]bool)

		for dec.More() {
			keyT, err := dec.Token()
			if err != nil {
				return nil, ErrInvalidContextMemory
			}
			key, ok := keyT.(string)
			if !ok {
				return nil, ErrInvalidContextMemory
			}
			if seenKeys[key] {
				return nil, ErrInvalidContextMemory
			}
			seenKeys[key] = true

			valT, err := dec.Token()
			if err != nil {
				return nil, ErrInvalidContextMemory
			}
			val, ok := valT.(string)
			if !ok {
				return nil, ErrInvalidContextMemory
			}

			switch key {
			case "context_id":
				ctxID = val
			case "class":
				class = val
			case "model":
				model = val
			default:
				return nil, ErrInvalidContextMemory
			}
		}

		// Read closing '}'
		t, err = dec.Token()
		if err != nil {
			return nil, ErrInvalidContextMemory
		}
		if delim, ok := t.(json.Delim); !ok || delim != '}' {
			return nil, ErrInvalidContextMemory
		}

		if len(seenKeys) != 3 {
			return nil, ErrInvalidContextMemory
		}

		if strings.TrimSpace(ctxID) == "" || strings.TrimSpace(class) == "" || strings.TrimSpace(model) == "" {
			return nil, ErrInvalidContextMemory
		}

		if seen[ctxID] {
			return nil, ErrInvalidContextMemory
		}
		seen[ctxID] = true

		m.preferences[ctxID] = RoutingPreference{Class: class, Model: model}
	}

	// Read closing ']'
	t, err = dec.Token()
	if err != nil {
		return nil, ErrInvalidContextMemory
	}
	if delim, ok := t.(json.Delim); !ok || delim != ']' {
		return nil, ErrInvalidContextMemory
	}

	if dec.More() {
		return nil, ErrInvalidContextMemory
	}

	return m, nil
}
