// Package governor (throttle_subscribe.go) implements ThrottleLadder's
// in-package stage-change subscription API (18-T0-RULINGS-R16.md
// R-16.64): Subscribe(ctx) (<-chan ThrottleEvent, cancel func()). No
// consumer is wired at this ticket's composition point - R-16.64 exists
// precisely so later tickets can wire consumers (conductor, scheduler,
// event bus, RPC, doctor) at their own composition points without this
// package depending on any of them.
//
// Purpose: subscriber (one registered channel) and Subscribe/publish, the
//
//	registration and broadcast halves of the same mechanism.
//
// Constraints: publish and a subscriber's own cancel must never race a
//
//	send against a close - both take subMu, so a channel is never closed
//	while publish might still be sending to it (Go panics on a send to a
//	closed channel, and this package must never risk that under a
//	slow or absent consumer). A slow consumer drops events rather than
//	blocking the ladder's tick goroutine: the buffered channel's send is
//	non-blocking (select/default).
//
// SPORT: internal/fleet/governor.ThrottleLadder (ADD, per T-3
//
//	sport_updates).
package governor

import (
	"context"
	"sync"
)

// subscriberBuffer is how many undelivered ThrottleEvents a subscriber's
// channel holds before publish starts dropping the newest event for
// that subscriber. Stage transitions are rare (they require sustained
// pressure change, never one per tick) so this only matters for a
// consumer that has stopped reading entirely, which is itself the
// signal to call cancel.
const subscriberBuffer = 8

// subscriber is one Subscribe registration.
type subscriber struct {
	ch   chan ThrottleEvent
	stop chan struct{}
	once sync.Once
}

// Subscribe registers a new listener for every future stage transition
// and returns its delivery channel plus a cancel func. Every transition
// after registration is delivered to every still-registered subscriber
// (subject to subscriberBuffer's drop-when-full policy); cancel stops
// delivery to this channel and closes it, and ctx.Done() does the same
// automatically. Calling cancel (or ctx being cancelled) leaves no
// goroutine running: the watcher goroutine this starts exits via
// whichever of ctx.Done() or an explicit cancel() happens first, and
// cancel is idempotent via sync.Once.
func (l *ThrottleLadder) Subscribe(ctx context.Context) (<-chan ThrottleEvent, func()) {
	sub := &subscriber{
		ch:   make(chan ThrottleEvent, subscriberBuffer),
		stop: make(chan struct{}),
	}
	l.subMu.Lock()
	id := l.nextSub
	l.nextSub++
	l.subs[id] = sub
	l.subMu.Unlock()

	cancel := func() {
		sub.once.Do(func() {
			close(sub.stop)
			l.subMu.Lock()
			delete(l.subs, id)
			close(sub.ch)
			l.subMu.Unlock()
		})
	}
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-sub.stop:
		}
	}()
	return sub.ch, cancel
}

// publish delivers ev to every currently-registered subscriber. Holding
// subMu for the whole delivery loop is what makes this race-free against
// a concurrent cancel: cancel cannot close a subscriber's channel while
// publish might still be sending to it, because both hold the same lock.
func (l *ThrottleLadder) publish(ev ThrottleEvent) {
	l.subMu.Lock()
	defer l.subMu.Unlock()
	for _, sub := range l.subs {
		select {
		case sub.ch <- ev:
		default:
		}
	}
}
