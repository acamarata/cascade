package supervision

// Purpose (this file): Detector — the stall detector's composition
// point. Wires ProgressTracker (stall_tracker.go) as the escalation
// ladder's ConfidenceProvider, wires stallSupervisor/stallNotifier
// (stall_escalation.go) as its SupervisorCreator/HumanNotifier, accepts
// Retryer/ContextEnricher/journal.Store as injected seams (satisfied at
// the daemon composition root, per stall_escalation.go's header), and
// drives the whole thing from two bus subscriptions
// (fleet.sessions.changed, jobs.gate.denied) plus a ≤1Hz poll loop
// (matching the M/S-26.T1 sampler's own Ticker discipline — never a
// bare time.Sleep).
//
// Inputs: StallSignal observations (Observe) and periodic Poll calls.
// Outputs: EscalationLadder.Advance calls, which themselves journal via
// M/S-27.T1 and, at the supervisor/human rungs, reach the S-39.T1
// attention queue and the I/S-18.T3 approval queue.
//
// SPORT: fleet.supervision.stall/ADDED (P1-E18-W4-S39-T5).

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/runtime"
)

// gateDeniedNamespace/Kind name the AC/S-60.T3 bus stream this ticket
// subscribes to. W4 has no live publisher yet (R-16.73's own acceptance
// criterion: "W4 verification is SYNTHETIC ONLY"); the subscription and
// its normalization exist regardless, so AC/S-60.T3 needs no further
// change here once it starts publishing.
const (
	gateDeniedNamespace                   = "jobs.gate"
	gateDeniedKind       events.EventKind = "jobs.gate.denied"
	stallCursorName                       = "supervision:stall"
	gateCursorName                        = "supervision:stall-gate"
	stallSubscribeBuffer                  = 64
)

// gateDeniedPayload is the jobs.gate.denied wire shape the ticket names:
// {job_id, session_id, ticket_id, reason, attempt}. ticket_id and reason
// are decoded but unused by this detector (R-16.73 keys the counting
// rule on job_id alone); they are named here only so a malformed payload
// missing them is not mistaken for one missing job_id/session_id.
type gateDeniedPayload struct {
	JobID     string `json:"job_id"`
	SessionID string `json:"session_id"`
	TicketID  string `json:"ticket_id"`
	Reason    string `json:"reason"`
	Attempt   int    `json:"attempt"`
}

// Detector is the stall detector. The zero value is not usable;
// construct with NewDetector.
type Detector struct {
	tracker *ProgressTracker
	ladder  *governor.EscalationLadder
	clock   runtime.Clock
	alive   atomic.Bool

	mu   sync.Mutex
	last map[string]StallEvent // sessionID -> most recent StallEvent
}

// Alive reports whether Run's delivery loop is currently subscribed —
// doctorcheck.go-style liveness probe, mirroring subscribe.go's
// Subscription.Alive.
func (d *Detector) Alive() bool { return d.alive.Load() }

// NewDetector builds a Detector. j, retryer, and enricher are the
// composition-root-supplied seams this ticket does not own (see
// stall_escalation.go's header); store and requester are the two seams
// this ticket does own. threshold is the idle/stall cutoff, wired from
// the C/S-05.T8 config-write surface at the composition root
// (08-INIT-CONFIG-SPEC §3) — this ticket's files_scope has no config
// file, so the value arrives here as a plain constructor argument rather
// than this package reading config itself. clock is injected (Art.7.3 —
// production callers pass runtime.NewSystemClock(), tests always pass a
// *runtime.FixedClock).
func NewDetector(j journal.Store, retryer governor.Retryer, enricher governor.ContextEnricher, store *Store, requester ApprovalRequester, policy governor.EscalationPolicy, threshold time.Duration, clock runtime.Clock) *Detector {
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	d := &Detector{
		tracker: NewProgressTracker(clock, threshold),
		clock:   clock,
		last:    make(map[string]StallEvent),
	}
	supervisor := &stallSupervisor{store: store, lookup: d.lookupStallEvent}
	notifier := &stallNotifier{requester: requester, lookup: d.lookupStallEvent}
	d.ladder = governor.NewEscalationLadder(j, d.tracker, retryer, enricher, supervisor, notifier, policy, clock)
	return d
}

// SetThreshold updates the idle/stall cutoff, for the config-write
// surface's hot-reload path (08 §3) to call without rebuilding the
// detector.
func (d *Detector) SetThreshold(threshold time.Duration) {
	d.tracker.setThreshold(threshold)
}

// lookupStallEvent implements stallLookup for the two escalation seams.
func (d *Detector) lookupStallEvent(sessionID string) (StallEvent, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ev, ok := d.last[sessionID]
	return ev, ok
}

// recordAndAdvance stores ev as sessionID's most recent StallEvent and
// invokes the escalation ladder. Advance is itself idempotent (per-entity
// terminal guard, escalation.go) and itself cancels an in-flight
// escalation once ProgressTracker.Confidence reports the session
// recovered — that is Touch's own effect on the very next Advance call,
// requiring no explicit cancellation here.
func (d *Detector) recordAndAdvance(ctx context.Context, ev StallEvent) error {
	d.mu.Lock()
	d.last[ev.SessionID] = ev
	d.mu.Unlock()
	return d.ladder.Advance(ctx, ev.SessionID)
}

// Touch records progress for sessionID, per L/S-24.T3's session event
// stream. A session that resumes activity before the human rung fires
// recovers via ProgressTracker.Confidence on the next Advance call.
func (d *Detector) Touch(sessionID string) {
	d.tracker.Touch(sessionID)
}

// Observe processes one normalized StallSignal (R-16.73): a blocked
// signal escalates immediately; a gate-denied signal escalates only once
// ProgressTracker.RecordGateDenied reports the 3-in-30-minute rule has
// fired for its job id. An invalid signal is refused and never
// escalates.
func (d *Detector) Observe(ctx context.Context, sig StallSignal) error {
	if err := sig.Validate(); err != nil {
		return err
	}
	now := sig.At
	if sig.Kind == SignalGateDenied {
		if !d.tracker.RecordGateDenied(sig.JobID, now) {
			return nil
		}
	}
	ev := StallEvent{SessionID: sig.SessionID, StallKind: signalToStallKind(sig.Kind), StalledSince: now}
	return d.recordAndAdvance(ctx, ev)
}

// Poll classifies every tracked session against the idle threshold and,
// for each currently stalled or unknown one, records a StallEvent and
// invokes the escalation ladder. A ProgressUnknown session (the event
// source is unavailable — "could not tell") still escalates, fail-closed,
// but is recorded with StallKindUnknown rather than StallKindIdle, so the
// distinction survives into whatever the supervisor-task/human rungs
// show a person.
//
// A per-session escalation outcome (EscalationExhausted, a rung failure)
// never stops the loop from reaching the remaining sessions; Poll
// reports only the first error that is NEITHER of those two expected
// outcomes, after every tracked session has been considered.
func (d *Detector) Poll(ctx context.Context) error {
	now := d.clock.Now().UnixMilli()
	var firstErr error
	for _, sessionID := range d.tracker.Watched() {
		status, since := d.tracker.Status(sessionID)
		var ev StallEvent
		switch status {
		case ProgressHealthy:
			continue
		case ProgressStalled:
			ev = StallEvent{SessionID: sessionID, StallKind: StallKindIdle, StalledSince: since, ElapsedSeconds: (now - since) / 1000}
		case ProgressUnknown:
			ev = StallEvent{SessionID: sessionID, StallKind: StallKindUnknown}
		default:
			ev = StallEvent{SessionID: sessionID, StallKind: StallKindUnknown}
		}
		err := d.recordAndAdvance(ctx, ev)
		if err != nil && !errors.Is(err, governor.EscalationExhausted) && !errors.Is(err, governor.ErrEscalationRungFailed) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Run drives the detector's two bus subscriptions until ctx is done. It
// marks the tracker's source unavailable for the duration of either
// subscribe call failing or either subscription's Errs firing, mirroring
// subscribe.go's Subscription.Run shape (bounded selects only, never an
// unguarded receive).
func (d *Detector) Run(ctx context.Context, bus SubscriberBus) error {
	sessSub, err := bus.Subscribe(ctx, sessionsChangedNamespace, stallCursorName, stallSubscribeBuffer)
	if err != nil {
		d.tracker.MarkSourceUnavailable()
		return err
	}
	defer func() { _ = sessSub.Unsubscribe() }()
	gateSub, err := bus.Subscribe(ctx, gateDeniedNamespace, gateCursorName, stallSubscribeBuffer)
	if err != nil {
		d.tracker.MarkSourceUnavailable()
		return err
	}
	defer func() { _ = gateSub.Unsubscribe() }()
	d.tracker.MarkSourceAvailable()
	d.alive.Store(true)
	defer d.alive.Store(false)
	defer d.tracker.MarkSourceUnavailable()

	for {
		select {
		case ev, open := <-sessSub.Events:
			if !open {
				return nil
			}
			d.handleSession(ctx, ev)
		case ev, open := <-gateSub.Events:
			if !open {
				return nil
			}
			d.handleGate(ctx, ev)
		case <-sessSub.Errs:
			return nil
		case <-gateSub.Errs:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

// handleSession decodes ev as a sessions.SessionRecord: any record
// touches progress, and a Blocked state additionally observes a
// SignalBlocked. A malformed payload is swallowed (best-effort, matching
// subscribe.go's identical rationale) rather than aborting the whole
// subscription over one bad event.
func (d *Detector) handleSession(ctx context.Context, ev events.Event) {
	if ev.Kind != sessionsChangedKind {
		return
	}
	var rec sessions.SessionRecord
	if err := json.Unmarshal(ev.Payload, &rec); err != nil {
		return
	}
	d.Touch(rec.SessionID)
	if state, ok := sessions.ParseSessionState(rec.State); ok && state == sessions.StateBlocked {
		_ = d.Observe(ctx, StallSignal{Kind: SignalBlocked, SessionID: rec.SessionID, At: d.clock.Now().UnixMilli()})
	}
}

// handleGate decodes ev as a gateDeniedPayload and observes a
// SignalGateDenied. A malformed payload is swallowed, matching
// handleSession's rationale.
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
	_ = d.Observe(ctx, StallSignal{Kind: SignalGateDenied, SessionID: payload.SessionID, JobID: payload.JobID, At: d.clock.Now().UnixMilli()})
}

// RunPoll drives Poll on every tick from the injected runtime.Ticker,
// matching the M/S-26.T1 sampler's own Ticker discipline (≤1Hz idle,
// never a bare time.Sleep). It returns when ctx is done.
func (d *Detector) RunPoll(ctx context.Context, ticker runtime.Ticker) {
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			_ = d.Poll(ctx)
		}
	}
}
