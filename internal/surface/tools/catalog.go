// Package tools provides in-memory registries for tool catalogs and MCP entries.
package tools

import (
	"errors"
	"sort"
	"strings"
)

// ErrInvalidInput is returned when a registry operation receives invalid input.
var ErrInvalidInput = errors.New("invalid tool registry input")

// ErrDuplicate is returned when a duplicate entry is registered or added.
var ErrDuplicate = errors.New("duplicate tool registry entry")

// ErrNotFound is returned when an entry to remove is not found.
var ErrNotFound = errors.New("tool registry entry not found")

// Catalog stores a set of unique tool names.
type Catalog struct {
	names map[string]struct{}
}

// NewCatalog creates a new initialized Catalog.
func NewCatalog() *Catalog {
	return &Catalog{names: make(map[string]struct{})}
}

// Register adds a tool name to the catalog. It returns ErrInvalidInput if the
// name is empty or contains surrounding whitespace, and ErrDuplicate if the
// name is already registered.
func (c *Catalog) Register(name string) error {
	if c == nil {
		return ErrInvalidInput
	}
	if name == "" || name != strings.TrimSpace(name) {
		return ErrInvalidInput
	}
	if c.names == nil {
		c.names = make(map[string]struct{})
	}
	if _, exists := c.names[name]; exists {
		return ErrDuplicate
	}
	c.names[name] = struct{}{}
	return nil
}

// List returns a lexicographically sorted copy of all registered tool names.
// A zero-value Catalog returns a non-nil empty slice.
func (c *Catalog) List() []string {
	if c == nil || c.names == nil {
		return []string{}
	}
	out := make([]string, 0, len(c.names))
	for n := range c.names {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// MCPEntries stores MCP entries separated by kind ("server" or "client").
type MCPEntries struct {
	entries map[string]map[string]struct{}
}

// NewMCPEntries creates a new initialized MCPEntries.
func NewMCPEntries() *MCPEntries {
	return &MCPEntries{
		entries: map[string]map[string]struct{}{
			"server": {},
			"client": {},
		},
	}
}

func validKind(kind string) bool {
	return kind == "server" || kind == "client"
}

// Add registers an entry of the given kind and name. It returns ErrInvalidInput
// for invalid kinds or names, and ErrDuplicate if the entry already exists.
func (m *MCPEntries) Add(kind, name string) error {
	if m == nil {
		return ErrInvalidInput
	}
	if !validKind(kind) {
		return ErrInvalidInput
	}
	if name == "" || name != strings.TrimSpace(name) {
		return ErrInvalidInput
	}
	if m.entries == nil {
		m.entries = make(map[string]map[string]struct{})
	}
	if m.entries[kind] == nil {
		m.entries[kind] = make(map[string]struct{})
	}
	if _, exists := m.entries[kind][name]; exists {
		return ErrDuplicate
	}
	m.entries[kind][name] = struct{}{}
	return nil
}

// Remove deletes an entry of the given kind and name. It returns ErrInvalidInput
// for invalid kinds or names, and ErrNotFound if the entry does not exist.
func (m *MCPEntries) Remove(kind, name string) error {
	if m == nil {
		return ErrInvalidInput
	}
	if !validKind(kind) {
		return ErrInvalidInput
	}
	if name == "" || name != strings.TrimSpace(name) {
		return ErrInvalidInput
	}
	if m.entries == nil || m.entries[kind] == nil {
		return ErrNotFound
	}
	if _, exists := m.entries[kind][name]; !exists {
		return ErrNotFound
	}
	delete(m.entries[kind], name)
	return nil
}

// List returns a lexicographically sorted copy of all entry names for the given
// kind. It returns nil and ErrInvalidInput for invalid kinds.
func (m *MCPEntries) List(kind string) ([]string, error) {
	if !validKind(kind) {
		return nil, ErrInvalidInput
	}
	if m == nil || m.entries == nil || m.entries[kind] == nil {
		return []string{}, nil
	}
	out := make([]string, 0, len(m.entries[kind]))
	for n := range m.entries[kind] {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}
