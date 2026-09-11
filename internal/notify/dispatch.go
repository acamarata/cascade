// Purpose: the [notify] Config, the per-priority buffered queue set, and
//
//	the priority-ordered Dispatcher: Urgent drains before High before
//	Normal before Low, per candidate subscriber the R-16.5
//	ScopeDeliveryPredicate gates fan-out, and a per-drain delivery
//	ledger prevents dispatching the same Notification to the same
//	Subscriber twice within one Drain call.
//
// Inputs: Notifications enqueued by router.go (bus-decoded) or inbox.go
//
//	(Notifier.Deliver); the live Registry snapshot at drain time.
//
// Outputs: each eligible, matching Subscriber's Receive is called at most
//
//	once per Notification per Drain call; a Subscriber error is logged
//	and the Notification is re-queued for the next Drain call, never
//	blocking any other Subscriber.
//
// Constraints: no bare time.Now (clock is injected); a full priority
//
//	queue drops the newest Notification with a slog WARN rather than
//	blocking the enqueuing goroutine.
//
// SPORT: internal.notify.Dispatcher/ADDED, internal.notify.Config/ADDED
//
//	(P1-E23-W5-S49-T1).

package notify

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// Config holds the [notify] section's hot-reloadable keys (08-INIT-CONFIG-
// SPEC §3; R-14.109 ratifies these literal defaults).
type Config struct {
	DispatchTimeout   time.Duration
	BufferUrgent      int
	BufferHigh        int
	BufferNormal      int
	BufferLow         int
	PriorityThreshold Priority
}

// DefaultConfig returns the [notify] defaults applied when the section is
// missing from the TOML config: dispatch_timeout "5s", channel_buffer
// urgent/high/normal/low 64/128/256/256, priority_threshold "low" (every
// Priority dispatched).
func DefaultConfig() Config {
	return Config{
		DispatchTimeout:   5 * time.Second,
		BufferUrgent:      64,
		BufferHigh:        128,
		BufferNormal:      256,
		BufferLow:         256,
		PriorityThreshold: PriorityLow,
	}
}

// priorityOrder is the fixed drain order: Urgent, High, Normal, Low.
var priorityOrder = [4]Priority{PriorityUrgent, PriorityHigh, PriorityNormal, PriorityLow}

// queueSet holds one buffered channel per Priority.
type queueSet struct {
	queues [4]chan Notification
	log    *slog.Logger
}

func newQueueSet(cfg Config, log *slog.Logger) *queueSet {
	return &queueSet{
		queues: [4]chan Notification{
			PriorityUrgent: make(chan Notification, cfg.BufferUrgent),
			PriorityHigh:   make(chan Notification, cfg.BufferHigh),
			PriorityNormal: make(chan Notification, cfg.BufferNormal),
			PriorityLow:    make(chan Notification, cfg.BufferLow),
		},
		log: log,
	}
}

// enqueue places n on its priority's queue, or on Low's queue when n
// carries an invalid Priority (fail closed toward the least-urgent lane,
// never dropped silently). A full queue drops n with a slog WARN rather
// than blocking the caller.
func (q *queueSet) enqueue(n Notification) {
	p := n.Priority
	if !p.Valid() {
		p = PriorityLow
	}
	select {
	case q.queues[p] <- n:
	default:
		q.log.Warn("notify: priority queue full, dropping notification",
			"priority", p.String(), "id", n.ID, "source", n.Source)
	}
}

// ledgerKey identifies one (Notification, Subscriber) delivery within a
// single Drain call.
type ledgerKey struct {
	notificationID string
	subscriberID   string
}

// Dispatcher drains queueSet in priority order and fans each Notification
// out to every matching, scope-eligible Subscriber.
type Dispatcher struct {
	queues   *queueSet
	registry *Registry
	inbox    *Inbox
	clock    runtime.Clock
	log      *slog.Logger

	mu sync.Mutex
}

// NewDispatcher returns a Dispatcher draining queues into registry's
// subscribers, recording outcomes in inbox.
func NewDispatcher(queues *queueSet, registry *Registry, inbox *Inbox, clock runtime.Clock, log *slog.Logger) *Dispatcher {
	return &Dispatcher{queues: queues, registry: registry, inbox: inbox, clock: clock, log: log}
}

// Drain performs one full drain cycle: every Notification currently
// queued, across all four priorities in Urgent->High->Normal->Low order,
// is fanned out exactly once per eligible Subscriber. It returns the
// number of Notifications processed (delivered, withheld, or expired).
// Drain does not block waiting for new enqueues; call it on a ticker or a
// tight loop with its own idle backoff.
func (d *Dispatcher) Drain(ctx context.Context) int {
	d.mu.Lock()
	defer d.mu.Unlock()

	ledger := make(map[ledgerKey]bool)
	var retryQueue []Notification
	processed := 0
	for _, p := range priorityOrder {
		processed += d.drainPriority(ctx, p, ledger, &retryQueue)
	}
	// Re-queued AFTER the full cycle, not fed back into the channel
	// mid-drain: a channel re-enqueue would let drainPriority's own loop
	// immediately re-observe the same notification in this SAME cycle,
	// where the ledger would then silently swallow it (already marked
	// delivered-to this subscriber) instead of genuinely waiting for the
	// next Drain call, defeating the "re-delivery at the next drain tick"
	// contract.
	for _, n := range retryQueue {
		d.queues.enqueue(n)
	}
	return processed
}

func (d *Dispatcher) drainPriority(ctx context.Context, p Priority, ledger map[ledgerKey]bool, retryQueue *[]Notification) int {
	ch := d.queues.queues[p]
	n := 0
	for {
		select {
		case <-ctx.Done():
			return n
		case notif := <-ch:
			d.fanOut(notif, ledger, retryQueue)
			n++
		default:
			return n
		}
	}
}

// fanOut delivers notif to every eligible Subscriber, or records it as
// expired if it is past ExpiresAt. A Subscriber whose predicate fails is
// withheld and counted; a Subscriber whose Receive errors is logged and
// appends notif to retryQueue for re-delivery on the NEXT Drain call,
// without blocking delivery to any other Subscriber this cycle.
func (d *Dispatcher) fanOut(notif Notification, ledger map[ledgerKey]bool, retryQueue *[]Notification) {
	now := d.clock.Now()
	if notif.expired(now) {
		d.inbox.recordExpired(notif)
		return
	}

	delivered := false
	retry := false
	for _, reg := range d.registry.Snapshot() {
		key := ledgerKey{notificationID: notif.ID, subscriberID: reg.Sub.ID()}
		if ledger[key] {
			continue
		}
		if !ScopeDeliveryPredicate(reg.Session, notif) {
			d.inbox.recordWithheld(notif, reg.Session.SessionID)
			continue
		}
		if !reg.Sub.Match(notif) {
			continue
		}
		ledger[key] = true
		if err := reg.Sub.Receive(notif); err != nil {
			d.log.Error("notify: subscriber receive failed, will re-deliver next drain",
				"subscriber", reg.Sub.ID(), "notification", notif.ID, "error", err)
			retry = true
			continue
		}
		delivered = true
	}

	notif.Sent = delivered
	d.inbox.record(notif)

	if retry {
		*retryQueue = append(*retryQueue, notif)
	}
}
