package background

import (
	"errors"
	"sync"
)

// ErrInvalidRecovery is returned when recovery input is invalid or the recoverer is closed.
var ErrInvalidRecovery = errors.New("invalid recovery input")

// SlotRecoverer manages slot recovery.
type SlotRecoverer struct {
	mu         sync.Mutex
	listDirs   func() ([]string, error)
	processDir func(string) error
	capacity   int
	closed     bool
}

// NewSlotRecoverer creates a new SlotRecoverer with the given callbacks and capacity.
// It returns ErrInvalidRecovery if callbacks are nil or capacity is negative.
func NewSlotRecoverer(listDirs func() ([]string, error), processDir func(string) error, capacity int) (*SlotRecoverer, error) {
	if listDirs == nil || processDir == nil || capacity < 0 {
		return nil, ErrInvalidRecovery
	}
	return &SlotRecoverer{
		listDirs:   listDirs,
		processDir: processDir,
		capacity:   capacity,
	}, nil
}

// Recover performs the slot recovery.
func (r *SlotRecoverer) Recover() error {
	if r == nil {
		return ErrInvalidRecovery
	}

	r.mu.Lock()
	if r.closed || r.listDirs == nil || r.processDir == nil {
		r.mu.Unlock()
		return ErrInvalidRecovery
	}
	listDirs := r.listDirs
	processDir := r.processDir
	r.mu.Unlock()

	dirs, err := listDirs()
	if err != nil {
		r.markClosed()
		return err
	}

	seen := make(map[string]struct{}, len(dirs))
	for _, d := range dirs {
		if _, ok := seen[d]; d == "" || ok {
			r.markClosed()
			return ErrInvalidRecovery
		}
		seen[d] = struct{}{}
	}

	for _, d := range dirs {
		if err := processDir(d); err != nil {
			r.markClosed()
			return err
		}
	}

	r.markClosed()
	return nil
}

// markClosed marks the recoverer as closed.
func (r *SlotRecoverer) markClosed() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
}
