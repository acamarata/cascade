// Package hj provides local lifecycle store primitives.
package hj

import (
	"errors"
	"sort"
	"strings"
	"sync"
)

// ReceiptState represents the lifecycle state of a receipt.
type ReceiptState string

const (
	Queue          ReceiptState = "Queue"
	Taken          ReceiptState = "Taken"
	ReceiptAttempt ReceiptState = "Attempt"
	Fail           ReceiptState = "Fail"
	TurnEnded      ReceiptState = "TurnEnded"
	End            ReceiptState = "End"
	Ready          ReceiptState = "Ready"
	Due            ReceiptState = "Due"
	For            ReceiptState = "For"
	CountsFor      ReceiptState = "CountsFor"
	Prune          ReceiptState = "Prune"
)

// Receipt represents a process-local receipt with a lifecycle state.
type Receipt struct {
	ID    string
	State ReceiptState
}

// ReceiptStore defines the interface for storing and retrieving receipts.
type ReceiptStore interface {
	Save(r Receipt) error
	All() []Receipt
}

var (
	// ErrInvalidReceiptState is returned when a receipt has an unknown state.
	ErrInvalidReceiptState = errors.New("receipt: invalid state")
	// ErrInvalidReceipt is returned when a receipt is missing required fields.
	ErrInvalidReceipt = errors.New("receipt: invalid receipt")
	// ErrDuplicateReceipt is returned when a receipt with the same ID already exists.
	ErrDuplicateReceipt = errors.New("receipt: duplicate id")
)

type memoryReceiptStore struct {
	mu       sync.Mutex
	receipts map[string]Receipt
}

// NewMemoryReceiptStore returns an independent empty mutex-protected in-memory implementation of ReceiptStore.
func NewMemoryReceiptStore() ReceiptStore {
	return &memoryReceiptStore{
		receipts: make(map[string]Receipt),
	}
}

// Save validates and stores a receipt in memory. It checks invalid state first,
// then empty ID (after trimming whitespace), then duplicate ID. The original ID is preserved.
func (m *memoryReceiptStore) Save(r Receipt) error {
	switch r.State {
	case Queue, Taken, ReceiptAttempt, Fail, TurnEnded, End, Ready, Due, For, CountsFor, Prune:
		// valid
	default:
		return ErrInvalidReceiptState
	}

	if strings.TrimSpace(r.ID) == "" {
		return ErrInvalidReceipt
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.receipts[r.ID]; exists {
		return ErrDuplicateReceipt
	}

	m.receipts[r.ID] = r
	return nil
}

// All returns independent snapshots of all receipts sorted ascending by ID, nil when empty.
func (m *memoryReceiptStore) All() []Receipt {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.receipts) == 0 {
		return nil
	}

	res := make([]Receipt, 0, len(m.receipts))
	for _, r := range m.receipts {
		res = append(res, r)
	}

	sort.Slice(res, func(i, j int) bool {
		return res[i].ID < res[j].ID
	})

	return res
}

var defaultStore = NewMemoryReceiptStore()

// Persist writes a receipt to the package-local default store.
// It returns its input on successful Save, otherwise Receipt{} and the Save error.
func Persist(r Receipt) (Receipt, error) {
	if err := defaultStore.Save(r); err != nil {
		return Receipt{}, err
	}
	return r, nil
}

// Receipts returns independent snapshots of all receipts from the default store
// sorted ascending by ID, nil when empty. Concurrent Persist and Receipts calls are safe.
func Receipts() []Receipt {
	return defaultStore.All()
}
