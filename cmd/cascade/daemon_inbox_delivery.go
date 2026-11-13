//go:build !windows

// Purpose: the subscriber lifecycle of addressed inbox delivery. Every live
//
//	session in the fleet sessions store is subscribed to the daemon's one
//	notify.Service through plugins.WireClaudeInboxPush: the sessions read at
//	start, then each fleet.sessions change on the bus (an active upsert
//	subscribes once, a closed session unsubscribes).
//
// Inputs: the daemon wiring (Ctx, Store, Clock, Bus, Manifest, Deps.Notify).
// Outputs: sessionSubscriber (Subscribed lists the subscribed session ids);
//
//	the inbox.delivery supervised loop; the inbox.push Skipped entry.
//
// Constraints: no second notify.Service or registry is built here: the
//
//	registry is w.Deps.Notify's. Every subscriber id is a session_id read
//	from the sessions store, never from the environment. An unsubscribe
//	runs before the session leaves Subscribed(). The push path stays
//	flag-off (D-S73T1-1). The loop starts through Manifest.GoSupervised.
//
// SPORT: cmd/cascade daemon inbox delivery (P1-HRN-07).
package main

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"sync"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	// inboxDeliverySubsystem names the supervised subscription loop.
	inboxDeliverySubsystem = "inbox.delivery"
	// inboxPushSubsystem names the channel push path, shipped flag-off.
	inboxPushSubsystem = "inbox.push"
	// inboxPushOffReason is the recorded flag-off reason.
	inboxPushOffReason = "flag-off: no server-observable delivery signal (D-S73T1-1)"
	// inboxDeliveryCursor is the loop's durable bus cursor name.
	inboxDeliveryCursor = "inbox-delivery"
	// inboxDeliveryBuffer bounds how far the loop may fall behind the bus.
	inboxDeliveryBuffer = 64
	// sessionsChangedKind is the fleet.sessions record-change event kind.
	sessionsChangedKind = events.EventKind("fleet.sessions.changed")
)

// deliverySessionID is the hydration queue's session id rule; an id outside
// it is never subscribed (fail closed).
var deliverySessionID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// errSubscriptionLost reports that the fleet.sessions subscription ended
// while the run context was still live.
var errSubscriptionLost = cascade.New(cascade.KindUnavailable, "inbox delivery: fleet.sessions subscription lost")

// sessionSubscriber keeps one notify subscription per live session.
type sessionSubscriber struct {
	ctx      context.Context
	bridge   *plugins.ClaudeInboxBridge
	registry *notify.Registry

	mu          sync.Mutex
	unsubscribe map[string]func()
}

// newSessionSubscriber returns a subscriber whose deliveries run under ctx.
func newSessionSubscriber(ctx context.Context, bridge *plugins.ClaudeInboxBridge, registry *notify.Registry) *sessionSubscriber {
	return &sessionSubscriber{ctx: ctx, bridge: bridge, registry: registry, unsubscribe: make(map[string]func())}
}

// subscribe wires id once; a second call for a subscribed id is a no-op.
func (s *sessionSubscriber) subscribe(id string) error {
	if !deliverySessionID.MatchString(id) {
		return cascade.Newf(cascade.KindInvalidInput, "inbox delivery: session id %q outside the registration rule", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.unsubscribe[id]; ok {
		return nil
	}
	unwire, err := plugins.WireClaudeInboxPush(s.ctx, s.bridge, s.registry, notify.CandidateSession{SessionID: id})
	if err != nil {
		return err
	}
	s.unsubscribe[id] = unwire
	return nil
}

// remove unsubscribes id, if subscribed, before it leaves Subscribed().
func (s *sessionSubscriber) remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if unwire, ok := s.unsubscribe[id]; ok {
		unwire()
		delete(s.unsubscribe, id)
	}
}

// closeAll unsubscribes every session.
func (s *sessionSubscriber) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, unwire := range s.unsubscribe {
		unwire()
		delete(s.unsubscribe, id)
	}
}

// Subscribed returns the subscribed session ids, sorted.
func (s *sessionSubscriber) Subscribed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.unsubscribe))
	for id := range s.unsubscribe {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// apply follows one session record: closed unsubscribes, anything else
// subscribes. An id outside the registration rule is skipped.
func (s *sessionSubscriber) apply(rec sessions.SessionRecord) error {
	if rec.SessionID == "" {
		return nil
	}
	if rec.State == sessions.StateClosed.String() {
		s.remove(rec.SessionID)
		return nil
	}
	if err := s.subscribe(rec.SessionID); err != nil && !cascade.HasKind(err, cascade.KindInvalidInput) {
		return err
	}
	return nil
}

// seed subscribes every record of live whose state is not closed.
func (s *sessionSubscriber) seed(ctx context.Context, live *sessions.Store) error {
	recs, err := live.List(ctx, sessions.Filter{})
	if err != nil {
		return err
	}
	for _, rec := range recs {
		if err := s.apply(rec); err != nil {
			return err
		}
	}
	return nil
}

// follow applies each fleet.sessions change on sub until ctx ends (nil) or
// the subscription ends first (errSubscriptionLost, wrapping its error).
func (s *sessionSubscriber) follow(ctx context.Context, sub *events.Subscription) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-sub.Errs:
			if err != nil {
				return cascade.Wrap(cascade.KindUnavailable, err, errSubscriptionLost.Error())
			}
		case ev, ok := <-sub.Events:
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				return errSubscriptionLost
			}
			if err := s.applyEvent(ev); err != nil {
				return err
			}
		}
	}
}

// applyEvent decodes one record-change event and applies it; other kinds
// on the topic (lane health) are ignored.
func (s *sessionSubscriber) applyEvent(ev events.Event) error {
	if ev.Kind != sessionsChangedKind {
		return nil
	}
	var rec sessions.SessionRecord
	if err := json.Unmarshal(ev.Payload, &rec); err != nil {
		return nil
	}
	return s.apply(rec)
}

// startInboxDelivery builds the bridge, subscribes the live sessions, and
// starts the supervised loop that follows registrations and closes.
func startInboxDelivery(w *daemonWiring) (*sessionSubscriber, error) {
	if w.Store == nil || w.Bus == nil || w.Deps.Notify == nil {
		return nil, cascade.New(cascade.KindUnavailable, "inbox delivery: no daemon store, bus or notify runtime")
	}
	bridge, err := newInboxBridge(w.Ctx, w)
	if err != nil {
		return nil, err
	}
	sub, err := w.Bus.SubscribeFromHead(w.Ctx, sessions.Topic, inboxDeliveryCursor, inboxDeliveryBuffer)
	if err != nil {
		return nil, err
	}
	subs := newSessionSubscriber(w.Ctx, bridge, w.Deps.Notify.Service().Registry)
	if err := subs.seed(w.Ctx, sessions.New(w.Store, w.Clock, w.Bus)); err != nil {
		subs.closeAll()
		_ = sub.Unsubscribe()
		return nil, err
	}
	w.Manifest.GoSupervised(w.Ctx, inboxDeliverySubsystem, "following fleet.sessions registrations",
		func(ctx context.Context) error {
			defer subs.closeAll()
			defer func() { _ = sub.Unsubscribe() }()
			return subs.follow(ctx, sub)
		})
	return subs, nil
}

// wireInboxDelivery is the inbox-delivery registration body: start the
// subscriber lifecycle and record the push path flag-off. A start failure
// is recorded Failed; the daemon still boots.
func wireInboxDelivery(w *daemonWiring) error {
	w.Manifest.Skipped(inboxPushSubsystem, inboxPushOffReason)
	if _, err := startInboxDelivery(w); err != nil {
		w.Manifest.Failed(inboxDeliverySubsystem, err.Error())
	}
	return nil
}
