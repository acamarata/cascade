// Purpose: Subscribe/Unsubscribe (task 4) and the background deliverLoop
//
//	that actually pulls persisted events and pushes them to a
//	subscriber's channel, committing the durable cursor only after each
//	send succeeds. Split from bus.go under R-14.117's authorized-split
//	allowance (Art.10.3's 300-line cap).
//
// Constraints: deliverLoop is the ONLY writer of a subscription's cursor
//
//	(cursor.go's commitCursor) and the ONLY closer of its Events channel —
//	Unsubscribe/Close never close sub.ch directly, they only signal
//	sub.done and then wait on sub.stopped, so a subscriber ranging over
//	Events never observes a close racing a still-in-flight send. No bare
//	time.Now; deliverLoop's own Store calls run against context.Background()
//	because a background goroutine's lifetime is not tied to any one
//	caller's request context — see its own doc comment.
//
// SPORT: internal.events.Bus/ADDED (Subscribe/Unsubscribe) (P1-E03-W1-S04-T3).

package events

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// subscription is one active Subscribe call's private state. sub.ch and
// sub.errs are only ever written by deliverLoop; every other field is
// read-only after construction except lastDelivered, which only
// deliverLoop's own goroutine touches (no lock needed — see its doc
// comment).
type subscription struct {
	namespace string
	name      string

	ch      chan Event
	errs    chan error
	done    chan struct{} // closed by Unsubscribe/Close to signal stop
	stopped chan struct{} // closed by deliverLoop once it has fully exited

	lastDelivered uint64
}

// Subscription is the caller-facing handle Subscribe returns.
type Subscription struct {
	// Events delivers each event in strict Seq order. It is closed when
	// the subscription stops, whether via Unsubscribe, Bus.Close, or a
	// fatal delivery error (in which case Errs receives that error first).
	Events <-chan Event
	// Errs receives at most one fatal error — a Store failure deliverLoop
	// could not recover from — immediately before Events is closed.
	// Buffered size 1, so deliverLoop's send into it never blocks.
	Errs <-chan error

	bus       *Bus
	namespace string
	name      string
}

// Unsubscribe stops this subscription: see Bus.Unsubscribe.
func (s *Subscription) Unsubscribe() error {
	return s.bus.Unsubscribe(s.namespace, s.name)
}

// Subscribe registers cursorName as an active subscriber to namespace,
// starting delivery at cursorName's durably-committed cursor (0, "before
// the first event," if cursorName has never committed — task 3's
// open-or-create contract) and continuing to tail newly published events
// until Unsubscribe or Close. bufferSize sets Events' channel capacity —
// the bound on how far this subscriber may fall behind before its
// delivery goroutine blocks (see bus.go's package doc, "Backpressure").
//
// Subscribe returns a cascade.KindConflict error if namespace already has
// an active subscription under cursorName — two concurrent live
// subscriptions sharing one cursor would race each other's commits, which
// this package refuses rather than silently corrupting either.
func (b *Bus) Subscribe(ctx context.Context, namespace, cursorName string, bufferSize int) (*Subscription, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, cascade.New(cascade.KindUnavailable, "events: Subscribe called after Close")
	}
	log, err := b.namespaceLogLocked(ctx, namespace)
	if err != nil {
		b.mu.Unlock()
		return nil, err
	}
	if _, exists := log.subs[cursorName]; exists {
		b.mu.Unlock()
		return nil, cascade.Newf(cascade.KindConflict,
			"events: namespace %q cursor %q already has an active subscription", namespace, cursorName)
	}

	start, err := loadCursor(ctx, b.store, namespace, cursorName)
	if err != nil {
		b.mu.Unlock()
		return nil, err
	}

	sub := &subscription{
		namespace:     namespace,
		name:          cursorName,
		ch:            make(chan Event, bufferSize),
		errs:          make(chan error, 1),
		done:          make(chan struct{}),
		stopped:       make(chan struct{}),
		lastDelivered: start,
	}
	log.subs[cursorName] = sub
	b.mu.Unlock()

	go b.deliverLoop(sub)

	return &Subscription{
		Events:    sub.ch,
		Errs:      sub.errs,
		bus:       b,
		namespace: namespace,
		name:      cursorName,
	}, nil
}

// Unsubscribe stops namespace's cursorName subscription: it signals the
// delivery goroutine to exit and BLOCKS until that goroutine has fully
// stopped (its Events channel closed) before returning, so a caller never
// races a subscription's own cleanup. The subscription's persisted cursor
// is left exactly where it last committed — "release" means release the
// live goroutine and channel, never forget the durable replay position,
// which is the entire point of a NAMED cursor surviving to the next
// Subscribe call with the same name.
//
// Unsubscribe returns a cascade.KindNotFound error ("cursor miss") if
// cursorName has no active subscription in namespace.
func (b *Bus) Unsubscribe(namespace, cursorName string) error {
	b.mu.Lock()
	log, ok := b.logs[namespace]
	if !ok {
		b.mu.Unlock()
		return cascade.Newf(cascade.KindNotFound, "events: namespace %q has no active subscriptions", namespace)
	}
	sub, ok := log.subs[cursorName]
	if !ok {
		b.mu.Unlock()
		return cascade.Newf(cascade.KindNotFound, "events: namespace %q cursor %q is not currently subscribed", namespace, cursorName)
	}
	delete(log.subs, cursorName)
	b.mu.Unlock()

	close(sub.done)
	<-sub.stopped
	return nil
}

// Close stops every active subscription across every namespace and marks
// the Bus permanently unusable for further Publish/Subscribe calls. Close
// blocks until every subscription's delivery goroutine has fully exited.
// Calling Close more than once is a no-op.
func (b *Bus) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	var subs []*subscription
	for _, log := range b.logs {
		for _, sub := range log.subs {
			subs = append(subs, sub)
		}
		log.subs = nil
	}
	b.mu.Unlock()

	for _, sub := range subs {
		close(sub.done)
	}
	for _, sub := range subs {
		<-sub.stopped
	}
	return nil
}

// deliverLoop is the one per-subscription background goroutine Subscribe
// starts. It alternates between pulling every not-yet-delivered event from
// Store and sending each to sub.ch — committing the durable cursor
// immediately after each send succeeds, never before (bus.go's package
// doc: this ordering is what makes delivery at-least-once) — and, once
// caught up, blocking on the namespace's wake channel until the next
// Publish or a stop signal. It runs against context.Background() rather
// than any caller-supplied context because its lifetime is the
// subscription's own, not tied to the Subscribe call's context, which may
// already have returned.
func (b *Bus) deliverLoop(sub *subscription) {
	defer close(sub.stopped)
	ctx := context.Background()

	for {
		events, wake, err := b.pull(ctx, sub)
		if err != nil {
			sub.errs <- err
			close(sub.ch)
			return
		}

		for _, ev := range events {
			select {
			case sub.ch <- ev:
				if cerr := commitCursor(ctx, b.store, sub.namespace, sub.name, ev.Seq); cerr != nil {
					sub.errs <- cerr
					close(sub.ch)
					return
				}
				sub.lastDelivered = ev.Seq
			case <-sub.done:
				close(sub.ch)
				return
			}
		}

		select {
		case <-wake:
		case <-sub.done:
			close(sub.ch)
			return
		}
	}
}

// pull returns every event newer than sub.lastDelivered as of right now,
// plus a reference to the namespace's CURRENT wake channel — captured
// under b.mu so it is guaranteed to be the channel the next Publish call
// to this namespace closes, however that Publish interleaves with this
// scan (see bus.go's package doc for the full race argument: no lost
// wakeup is possible under this pairing).
func (b *Bus) pull(ctx context.Context, sub *subscription) ([]Event, <-chan struct{}, error) {
	b.mu.Lock()
	log := b.logs[sub.namespace]
	wake := log.wake
	b.mu.Unlock()

	events, err := replayFrom(ctx, b.store, sub.namespace, sub.lastDelivered)
	if err != nil {
		return nil, nil, err
	}
	return events, wake, nil
}
