// Package hj provides broker lifecycle management.
package hj

import "sync"

// Brokers manages job recovery, health checks and garbage collection
// using a shared mutex to serialize all operations.
type Brokers struct {
	brokersMu sync.Mutex
}

// NewBrokers returns an initialized Brokers instance.
func NewBrokers() *Brokers {
	return &Brokers{}
}

// Recover holds brokersMu for the entire recovery run.
// An optional work function may be provided, executed while the lock is held.
func (b *Brokers) Recover(work ...func()) {
	b.brokersMu.Lock()
	defer b.brokersMu.Unlock()
	for _, fn := range work {
		if fn != nil {
			fn()
		}
	}
}

// Health blocks on brokersMu until recovery ends, then runs the health check.
// An optional work function may be provided, executed while the lock is held.
func (b *Brokers) Health(work ...func()) {
	b.brokersMu.Lock()
	defer b.brokersMu.Unlock()
	for _, fn := range work {
		if fn != nil {
			fn()
		}
	}
}

// GC blocks on brokersMu until recovery ends, then runs garbage collection.
// An optional work function may be provided, executed while the lock is held.
func (b *Brokers) GC(work ...func()) {
	b.brokersMu.Lock()
	defer b.brokersMu.Unlock()
	for _, fn := range work {
		if fn != nil {
			fn()
		}
	}
}
