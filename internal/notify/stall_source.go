// Purpose: BusStallSource, the StallSource implementation that hands each
//
//	"supervision.stalled" bus event to away mode as one StallNotice.
//
// Inputs: the event bus (namespace "supervision"), read from its head over
//
//	the durable bus cursor StallCursor.
//
// Outputs: DrainStallNotices, which returns every notice decoded since the
//
//	previous call exactly once.
//
// Constraints: the payload names are the supervision publisher's
//
//	(events.NotificationPayload; ID "stall:<session>:<ms|unknown>",
//	CorrelationID the session). A notice is never lost between the bus and
//	Drain while the process lives: the bus commits its cursor when it hands an
//	event to this source, so Run, on every exit, stops the subscription and
//	decodes every event already handed (including those still buffered in the
//	subscription channel) before it returns. A duplicate ID inside the last
//	stallSeenCap IDs is dropped; the pending set is bounded (oldest dropped,
//	WARNed). A payload that does not decode is dropped with one WARN. Only the
//	stalled kind is read; other kinds on the namespace are ignored. A source
//	runs once: its subscription cannot be reopened. The cursor commit is the
//	bus's (bus_subscribe.go deliverLoop) and happens at handoff to this
//	source, not at Drain: a notice held in memory when the whole process dies
//	is not replayed (the bus has no acknowledge-later call to defer it).
//
// SPORT: internal/notify BusStallSource (ADD) — P1-HRN-10.

package notify

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/pkg/cascade"
)

// StallCursor is the durable bus cursor the stall source reads under.
const StallCursor = "notify-away-stall"

const (
	// stallNamespace and stallKind are the supervision publisher's bus
	// coordinates (internal/fleet/supervision/stall_publish.go).
	stallNamespace                  = "supervision"
	stallKind      events.EventKind = "supervision.stalled"
	// stallBuffer is the subscription channel capacity.
	stallBuffer = 64
	// stallSeenCap bounds the duplicate-ID memory; stallPendingCap bounds the
	// undrained notices.
	stallSeenCap    = 4096
	stallPendingCap = 4096
)

// BusStallSource implements StallSource over the event bus.
type BusStallSource struct {
	sub *events.Subscription
	log *slog.Logger

	started atomic.Bool

	mu       sync.Mutex
	pending  []StallNotice
	seen     map[string]struct{}
	seenRing [stallSeenCap]string // FIFO of remembered IDs; seenNext is the oldest slot
	seenNext int
	seenFull bool

	// afterHandle, set by tests before Run, observes each event once it has
	// been fully processed.
	afterHandle func(events.Event)
}

var _ StallSource = (*BusStallSource)(nil)

// NewBusStallSource subscribes to the supervision namespace from its head
// (a first start never replays history; a restart resumes past every event
// the bus already handed). A nil bus is refused. A nil log discards.
func NewBusStallSource(ctx context.Context, bus *events.Bus, log *slog.Logger) (*BusStallSource, error) {
	if bus == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "notify: stall source needs an event bus")
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	sub, err := bus.SubscribeFromHead(ctx, stallNamespace, StallCursor, stallBuffer)
	if err != nil {
		return nil, err
	}
	return &BusStallSource{sub: sub, log: log, seen: make(map[string]struct{}, stallSeenCap)}, nil
}

// Run reads the subscription until ctx ends, the subscription closes or the
// bus reports a fatal delivery error (returned as-is). On every exit it stops
// the subscription and decodes whatever the bus had already handed over, so
// the notices pending in the source are complete when Run returns. A
// cancelled ctx ends Run with a nil error. Run may be called once.
func (s *BusStallSource) Run(ctx context.Context) error {
	if !s.started.CompareAndSwap(false, true) {
		return cascade.New(cascade.KindConflict, "notify: stall source already ran; open a new one")
	}
	fatal := s.loop(ctx)
	stopErr := s.stop()
	if fatal != nil {
		return fatal
	}
	return stopErr
}

// loop handles events until ctx ends or the subscription stops. The context
// is checked first so a cancelled source never prefers a new event.
func (s *BusStallSource) loop(ctx context.Context) error {
	errs := s.sub.Errs
	for {
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-errs:
			if !ok {
				errs = nil // a closed channel would win every select
				continue
			}
			if err != nil {
				return err
			}
		case ev, ok := <-s.sub.Events:
			if !ok {
				return nil
			}
			s.handle(ev)
		}
	}
}

// stop ends the subscription and handles every event the bus had already
// handed to it. The bus commits its cursor when it hands an event over, so an
// event left in the channel here would be lost for good.
func (s *BusStallSource) stop() error {
	err := s.sub.Unsubscribe()
	// A bus that was closed first has already stopped the subscription.
	if err != nil && cascade.HasKind(err, cascade.KindNotFound) {
		err = nil
	}
	for ev := range s.sub.Events {
		s.handle(ev)
	}
	return err
}

// handle turns one bus event into at most one pending notice.
func (s *BusStallSource) handle(ev events.Event) {
	defer s.observe(ev)
	if ev.Kind != stallKind {
		return
	}
	p, err := events.DecodeNotificationPayload(ev.Payload)
	if err != nil {
		s.log.Warn("notify: dropping undecodable supervision.stalled payload", "seq", ev.Seq)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.seen[p.ID]; dup {
		return
	}
	s.remember(p.ID)
	s.pending = append(s.pending, StallNotice{NotificationID: p.ID, CorrelationID: p.CorrelationID})
	if over := len(s.pending) - stallPendingCap; over > 0 {
		s.log.Warn("notify: stall notices not drained; dropping the oldest", "dropped", over)
		s.pending = append([]StallNotice(nil), s.pending[over:]...)
	}
}

// remember records id in the bounded duplicate memory. Caller holds s.mu.
func (s *BusStallSource) remember(id string) {
	if s.seenFull {
		delete(s.seen, s.seenRing[s.seenNext])
	}
	s.seenRing[s.seenNext] = id
	s.seen[id] = struct{}{}
	s.seenNext++
	if s.seenNext == stallSeenCap {
		s.seenNext, s.seenFull = 0, true
	}
}

func (s *BusStallSource) observe(ev events.Event) {
	if s.afterHandle != nil {
		s.afterHandle(ev)
	}
}

// DrainStallNotices returns every notice decoded since the previous call, in
// event order, and clears the pending set: each notice is returned once.
func (s *BusStallSource) DrainStallNotices() []StallNotice {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.pending
	s.pending = nil
	return out
}
