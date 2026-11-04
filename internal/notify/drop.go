// Purpose: the dropped-item path. A Notification that finds its priority
//
//	queue full is recorded in the Inbox first, counted under its drop reason,
//	and reported to an optional DropSink. No body or payload leaves this file.
//
// Constraints: the sink must not block; the producer is never blocked.

package notify

import (
	"context"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// DropQueueFull is the drop reason for a Notification that found its
// priority queue full.
const DropQueueFull = "queue-full"

// DropEvent describes one dropped Notification. It carries identifiers and
// the reason only; never the notification body.
type DropEvent struct {
	ID       string
	Source   string
	Reason   string
	Priority Priority
	At       time.Time
}

// DropSink receives each DropEvent. It must not block.
type DropSink func(ctx context.Context, e DropEvent)

// dropCounter counts drops per reason and forwards each one to the sink.
type dropCounter struct {
	mu       sync.Mutex
	byReason map[string]uint64
	sink     DropSink
}

// setSink installs the sink that receives every later drop.
func (c *dropCounter) setSink(sink DropSink) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sink = sink
}

// count returns how many drops were recorded under reason.
func (c *dropCounter) count(reason string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.byReason[reason]
}

// record increments the counter for e.Reason and hands e to the sink.
func (c *dropCounter) record(e DropEvent) {
	c.mu.Lock()
	if c.byReason == nil {
		c.byReason = make(map[string]uint64)
	}
	c.byReason[e.Reason]++
	sink := c.sink
	c.mu.Unlock()
	if sink != nil {
		sink(context.Background(), e)
	}
}

// bind wires the inbox and clock a drop needs. NewDispatcher calls it once.
func (q *queueSet) bind(inbox *Inbox, clock runtime.Clock) {
	q.inbox = inbox
	q.clock = clock
}

// drop handles a Notification the priority queue p could not take: it is
// recorded in the Inbox first, counted under reason, reported to the sink,
// and logged once without its payload.
func (q *queueSet) drop(n Notification, p Priority, reason string) {
	if q.inbox != nil {
		q.inbox.record(n)
	}
	var at time.Time
	if q.clock != nil {
		at = q.clock.Now()
	}
	q.log.Warn("notify: priority queue full, dropping notification",
		"priority", p.String(), "id", n.ID, "source", n.Source)
	q.drops.record(DropEvent{ID: n.ID, Source: n.Source, Reason: reason, Priority: p, At: at})
}
