package uninstall

// Purpose: an in-memory custody for the tests that are not about the vault
//   (the real ForceFileVault custody is scrypt-backed and slow under -race).

import (
	"context"
	"sort"
	"sync"

	"github.com/acamarata/cascade/internal/secrets"
)

// memCustody is an in-memory custody for tests that are not about the vault.
type memCustody struct {
	name    string
	mu      sync.Mutex
	entries map[string][]byte
}

func newMemCustody(name string) *memCustody {
	return &memCustody{name: name, entries: map[string][]byte{}}
}

func (m *memCustody) Name() string    { return m.name }
func (m *memCustody) Available() bool { return true }
func (m *memCustody) Set(_ context.Context, name string, v []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[name] = v
	return nil
}
func (m *memCustody) Get(_ context.Context, name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.entries[name]
	if !ok {
		return nil, secrets.ErrSecretNotFound(name)
	}
	return v, nil
}
func (m *memCustody) Delete(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[name]; !ok {
		return secrets.ErrSecretNotFound(name)
	}
	delete(m.entries, name)
	return nil
}
func (m *memCustody) List(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.entries))
	for n := range m.entries {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}
