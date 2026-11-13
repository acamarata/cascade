package promise

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrNotFound is returned when a promise with the requested ID does not exist.
var ErrNotFound = errors.New("promise not found")

// Store defines the interface for managing promises.
type Store interface {
	// Submit adds a promise to the store. If a promise with the same
	// IdempotencyKey already exists, it returns that existing promise.
	Submit(ctx context.Context, p *Promise) (*Promise, error)

	// Get retrieves a promise by its ID. Returns ErrNotFound if not found.
	Get(ctx context.Context, id string) (*Promise, error)

	// Update persists changes to a promise's state, including heartbeat updates.
	Update(ctx context.Context, p *Promise) error

	// ListStale returns all promises with NextAt before the given time.
	ListStale(ctx context.Context, before time.Time) ([]*Promise, error)
}

// MemoryStore is an in-memory implementation of Store with thread-safe operations.
type MemoryStore struct {
	mu          sync.RWMutex
	promises    map[string]*Promise // keyed by ID
	idempotency map[string]*Promise // keyed by IdempotencyKey
}

// NewMemoryStore creates a new in-memory promise store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		promises:    make(map[string]*Promise),
		idempotency: make(map[string]*Promise),
	}
}

// Submit adds a promise to the store. If a promise with the same
// IdempotencyKey already exists, it returns that existing promise.
func (s *MemoryStore) Submit(_ context.Context, p *Promise) (*Promise, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if a promise with this IdempotencyKey already exists
	if existing, ok := s.idempotency[p.IdempotencyKey]; ok {
		return existing, nil
	}

	// Store the new promise
	s.promises[p.ID] = p
	s.idempotency[p.IdempotencyKey] = p

	return p, nil
}

// Get retrieves a promise by its ID. Returns ErrNotFound if not found.
func (s *MemoryStore) Get(_ context.Context, id string) (*Promise, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	p, ok := s.promises[id]
	if !ok {
		return nil, ErrNotFound
	}

	return p, nil
}

// Update persists changes to a promise's state, including heartbeat updates.
func (s *MemoryStore) Update(_ context.Context, p *Promise) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Verify the promise exists
	existing, ok := s.promises[p.ID]
	if !ok {
		return ErrNotFound
	}

	// Update the stored promise with new values
	existing.State = p.State
	existing.Heartbeat = p.Heartbeat
	existing.Attempts = p.Attempts
	existing.NextAt = p.NextAt
	existing.ResultRef = p.ResultRef

	return nil
}

// ListStale returns all promises with NextAt before the given time.
func (s *MemoryStore) ListStale(ctx context.Context, before time.Time) ([]*Promise, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var stale []*Promise
	for _, p := range s.promises {
		if p.NextAt.Before(before) {
			stale = append(stale, p)
		}
	}

	return stale, nil
}
