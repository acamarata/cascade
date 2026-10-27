// Purpose: a Custody selected on its first use, so a caller can hand a broker
// its custody before an elevated verb is authorized without selecting,
// opening or reading a vault first (P1-SEC-08).
// Inputs: an open func that selects the real custody.
// Outputs: LazyCustody, a Custody that calls open once, on the first method
// call, and keeps the result.
// Constraints: a failed or nil selection is remembered and returned by every
// later call; Name reports "none" and Available false for it. Nothing is
// selected until any method, Name and Available included, is called.
// SPORT: secrets.custody.lazy (ADD).

package secrets

import (
	"context"
	"sync"
)

// LazyCustody returns a Custody that selects through open on first use.
func LazyCustody(open func() (Custody, error)) Custody {
	return &lazyCustody{open: open}
}

type lazyCustody struct {
	open func() (Custody, error)
	mu   sync.Mutex
	done bool
	c    Custody
	err  error
}

func (l *lazyCustody) custody() (Custody, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.done {
		l.done = true
		if l.open == nil {
			l.err = ErrNoCustodyAvailable()
		} else {
			l.c, l.err = l.open()
		}
		if l.err == nil && l.c == nil {
			l.err = ErrNoCustodyAvailable()
		}
	}
	return l.c, l.err
}

// Name implements Custody; an unselectable custody reports "none".
func (l *lazyCustody) Name() string {
	c, err := l.custody()
	if err != nil {
		return "none"
	}
	return c.Name()
}

// Available implements Custody.
func (l *lazyCustody) Available() bool {
	c, err := l.custody()
	return err == nil && c.Available()
}

// Set implements Custody.
func (l *lazyCustody) Set(ctx context.Context, name string, value []byte) error {
	c, err := l.custody()
	if err != nil {
		return err
	}
	return c.Set(ctx, name, value)
}

// Get implements Custody.
func (l *lazyCustody) Get(ctx context.Context, name string) ([]byte, error) {
	c, err := l.custody()
	if err != nil {
		return nil, err
	}
	return c.Get(ctx, name)
}

// Delete implements Custody.
func (l *lazyCustody) Delete(ctx context.Context, name string) error {
	c, err := l.custody()
	if err != nil {
		return err
	}
	return c.Delete(ctx, name)
}

// List implements Custody.
func (l *lazyCustody) List(ctx context.Context) ([]string, error) {
	c, err := l.custody()
	if err != nil {
		return nil, err
	}
	return c.List(ctx)
}
