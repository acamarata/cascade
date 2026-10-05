package supervision

// Purpose (this file): the Detector's bus-driven delivery loop (Run) and
// the two event handlers it dispatches to (handleSession, handleGate),
// split out of stall.go to keep both files under the Art.10.3 300-line
// cap, plus the session watch rule they and Observe share.
//
// Inputs: two head-start bus subscriptions (fleet.sessions, jobs.gate) and,
// on a detector built by NewStallDetector, a SessionLookup.
// Outputs: tracker updates, episode ends and Observe calls.
// Constraints: handlers stamp every signal with the event's own timestamp,
// never the detector clock, so a replayed backlog keeps its age; a dead
// subscription is a typed error, never a silent nil.
//
// SPORT: fleet.supervision.stall/ADDED (P1-E18-W4-S39-T5).

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/pkg/cascade"
)

// StallBus is the subscribe side of the event bus Run needs. *events.Bus
// satisfies it. Both streams start at the head: a first start never sees
// history it was not configured for, while a committed cursor still
// resumes its backlog.
type StallBus interface {
	SubscribeFromHead(ctx context.Context, namespace, cursorName string, bufferSize int) (*events.Subscription, error)
}

// Run drives the detector's two bus subscriptions until ctx is done. It
// seeds the tracker from the session store (when it has one), marks the
// tracker's source unavailable for the duration of any subscribe failure,
// and returns a typed KindUnavailable error when a subscription dies
// before ctx is done. It returns nil only on ctx cancellation.
func (d *Detector) Run(ctx context.Context, bus StallBus) error {
	sessSub, err := bus.SubscribeFromHead(ctx, sessionsChangedNamespace, stallCursorName, stallSubscribeBuffer)
	if err != nil {
		d.tracker.MarkSourceUnavailable()
		return err
	}
	defer func() { _ = sessSub.Unsubscribe() }()
	gateSub, err := bus.SubscribeFromHead(ctx, gateDeniedNamespace, gateCursorName, stallSubscribeBuffer)
	if err != nil {
		d.tracker.MarkSourceUnavailable()
		return err
	}
	defer func() { _ = gateSub.Unsubscribe() }()
	defer d.tracker.MarkSourceUnavailable()
	if err := d.seed(ctx); err != nil {
		return err
	}
	d.tracker.MarkSourceAvailable()
	d.alive.Store(true)
	defer d.alive.Store(false)
	return d.deliver(ctx, sessSub, gateSub)
}

// deliver is Run's select loop (bounded selects only, never an unguarded
// receive).
func (d *Detector) deliver(ctx context.Context, sessSub, gateSub *events.Subscription) error {
	for {
		select {
		case ev, open := <-sessSub.Events:
			if !open {
				return subscriptionLost(ctx, nil)
			}
			d.handleSession(ctx, ev)
		case ev, open := <-gateSub.Events:
			if !open {
				return subscriptionLost(ctx, nil)
			}
			d.handleGate(ctx, ev)
		case err := <-sessSub.Errs:
			return subscriptionLost(ctx, err)
		case err := <-gateSub.Errs:
			return subscriptionLost(ctx, err)
		case <-ctx.Done():
			return nil
		}
	}
}

// subscriptionLost is Run's result when a subscription ends: nil if ctx is
// done (the expected shutdown path), else the typed lost-subscription error.
func subscriptionLost(ctx context.Context, cause error) error {
	if ctx.Err() != nil {
		return nil
	}
	return cascade.Wrap(cascade.KindUnavailable, cause, "stall detector subscription lost")
}

// watchedState reports whether a session in state s is watched for stalls:
// active, blocked and stalled. Every other state (idle, closed, unknown)
// is unwatched.
func watchedState(s sessions.SessionState) bool {
	return s == sessions.StateActive || s == sessions.StateBlocked || s == sessions.StateStalled
}

// progressMark is a record's last-progress instant (unix millis):
// max(UpdatedAt, LastToolAt, LastPromptAt).
func progressMark(rec sessions.SessionRecord) int64 {
	mark := rec.UpdatedAt
	for _, p := range []*int64{rec.LastToolAt, rec.LastPromptAt} {
		if p != nil && *p > mark {
			mark = *p
		}
	}
	return mark
}

// unwatch ends sessionID's tracking and stall episode.
func (d *Detector) unwatch(sessionID string) {
	d.tracker.Unwatch(sessionID)
	d.endEpisode(sessionID)
}

// seed loads every watched-state session from the session store so a
// session already quiet at start is tracked. A detector without a session
// lookup has nothing to seed.
func (d *Detector) seed(ctx context.Context) error {
	if d.sessions == nil {
		return nil
	}
	recs, err := d.sessions.List(ctx, sessions.Filter{})
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "stall detector could not seed from the session store")
	}
	now := d.clock.Now().UnixMilli()
	for _, rec := range recs {
		if state, ok := sessions.ParseSessionState(rec.State); ok && watchedState(state) && rec.SessionID != "" {
			mark := progressMark(rec)
			if mark <= 0 {
				mark = now
			}
			d.tracker.TouchAt(rec.SessionID, mark)
		}
	}
	return nil
}

// sessionWatched applies the watch rule through the session store: an
// unknown or unwatched session reports (false, nil). A detector without a
// lookup watches everything. A lookup failure other than not-found is a
// typed error, never a silent "watched".
func (d *Detector) sessionWatched(ctx context.Context, sessionID string) (bool, error) {
	if d.sessions == nil {
		return true, nil
	}
	rec, err := d.sessions.Get(ctx, sessionID)
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return false, nil
		}
		return false, cascade.Wrap(cascade.KindUnavailable, err, "stall detector session lookup failed")
	}
	state, ok := sessions.ParseSessionState(rec.State)
	return ok && watchedState(state), nil
}

// handleSession decodes ev as a sessions.SessionRecord. A watched-state
// record moves the session's progress mark to the record's own activity
// time (the event time when the record carries none), ending its stall
// episode if that is new progress; a Blocked state additionally observes a
// SignalBlocked. Any other state unwatches the session. A malformed
// payload is swallowed (best-effort, matching subscribe.go's identical
// rationale) rather than aborting the whole subscription over one bad
// event.
func (d *Detector) handleSession(ctx context.Context, ev events.Event) {
	if ev.Kind != sessionsChangedKind {
		return
	}
	var rec sessions.SessionRecord
	if err := json.Unmarshal(ev.Payload, &rec); err != nil || rec.SessionID == "" {
		return
	}
	state, ok := sessions.ParseSessionState(rec.State)
	if !ok || !watchedState(state) {
		d.unwatch(rec.SessionID)
		return
	}
	at := ev.Timestamp.UnixMilli()
	mark := progressMark(rec)
	if mark <= 0 {
		mark = at
	}
	if d.tracker.TouchAt(rec.SessionID, mark) {
		d.endEpisode(rec.SessionID)
	}
	if state == sessions.StateBlocked {
		_ = d.Observe(ctx, StallSignal{Kind: SignalBlocked, SessionID: rec.SessionID, At: at})
	}
}

// handleGate decodes ev as a gateDeniedPayload and observes a
// SignalGateDenied stamped with the event's own time. A denial older than
// the counting window can never count toward the 3-in-30-minute rule, so a
// backlog replayed after a restart stays quiet. A malformed payload is
// swallowed, matching handleSession's rationale.
func (d *Detector) handleGate(ctx context.Context, ev events.Event) {
	if ev.Kind != gateDeniedKind {
		return
	}
	var payload gateDeniedPayload
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return
	}
	if payload.JobID == "" || payload.SessionID == "" {
		return
	}
	at := ev.Timestamp.UnixMilli()
	if d.clock.Now().UnixMilli()-at > gateDeniedWindow.Milliseconds() {
		return
	}
	_ = d.Observe(ctx, StallSignal{Kind: SignalGateDenied, SessionID: payload.SessionID, JobID: payload.JobID, At: at})
}
