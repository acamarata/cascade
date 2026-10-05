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
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/fleet/journal"
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
// construct with NewDetector (legacy seams) or NewStallDetector (real
// rungs, publisher, session-store watch rule).
type Detector struct {
	tracker   *ProgressTracker
	ladder    *governor.EscalationLadder
	clock     runtime.Clock
	rungDelay time.Duration
	alive     atomic.Bool

	// Set only by NewStallDetector, before the detector is shared. A nil
	// pub or sessions disables episode publishing or the session watch
	// rule respectively; requireAlive makes Poll refuse while Run is not
	// subscribed.
	pub          StallPublisher
	sessions     SessionLookup
	requireAlive bool

	pubMu sync.Mutex // serializes the once-per-episode publish
	mu    sync.Mutex
	last  map[string]StallEvent // sessionID -> most recent StallEvent
	// published marks episodes whose supervision.stalled went out;
	// advanced is the clock instant of each session's last Advance;
	// epoch counts each session's ended episodes, so a stall record built
	// before an episode ended is recognised as stale.
	published map[string]bool
	advanced  map[string]time.Time
	epoch     map[string]uint64
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
		tracker:   NewProgressTracker(clock, threshold),
		clock:     clock,
		rungDelay: policy.RungDelay,
		last:      make(map[string]StallEvent),
		published: make(map[string]bool),
		advanced:  make(map[string]time.Time),
		epoch:     make(map[string]uint64),
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

// recordAndAdvance stores ev as sessionID's most recent StallEvent,
// publishes the episode's supervision.stalled once, and invokes the
// escalation ladder. Advance is itself idempotent (per-entity terminal
// guard, escalation.go) and itself cancels an in-flight escalation once
// ProgressTracker.Confidence reports the session recovered — that is
// Touch's own effect on the very next Advance call. A failed publish never
// blocks the advance; it is returned alongside the advance outcome and
// retried by the next call. epoch is the session's episodeEpoch read
// before ev was built: if the episode has ended since, ev is stale and
// nothing is recorded, published or advanced.
func (d *Detector) recordAndAdvance(ctx context.Context, ev StallEvent, epoch uint64) error {
	d.mu.Lock()
	if d.epoch[ev.SessionID] != epoch {
		d.mu.Unlock()
		return nil
	}
	d.last[ev.SessionID] = ev
	d.mu.Unlock()
	pubErr := d.publishOnce(ctx, ev, epoch)
	err := d.ladder.Advance(ctx, ev.SessionID)
	d.mu.Lock()
	if d.epoch[ev.SessionID] == epoch {
		d.advanced[ev.SessionID] = d.clock.Now()
	}
	d.mu.Unlock()
	switch {
	case pubErr == nil:
		return err
	case err == nil:
		return pubErr
	}
	return errors.Join(err, pubErr)
}

// Touch records progress for sessionID, per L/S-24.T3's session event
// stream, and ends its stall episode. A session that resumes activity
// before the human rung fires recovers via ProgressTracker.Confidence on
// the next Advance call.
func (d *Detector) Touch(sessionID string) {
	d.tracker.Touch(sessionID)
	d.endEpisode(sessionID)
}

// Observe processes one normalized StallSignal (R-16.73): a blocked
// signal escalates immediately; a gate-denied signal escalates only once
// ProgressTracker.RecordGateDenied reports the 3-in-30-minute rule has
// fired for its job id. An invalid signal is refused and never
// escalates. When the detector has a session lookup, a signal for an
// unknown or unwatched (idle, closed, ...) session is ignored.
func (d *Detector) Observe(ctx context.Context, sig StallSignal) error {
	if err := sig.Validate(); err != nil {
		return err
	}
	if ok, err := d.sessionWatched(ctx, sig.SessionID); err != nil || !ok {
		return err
	}
	epoch := d.episodeEpoch(sig.SessionID)
	now := sig.At
	if sig.Kind == SignalGateDenied {
		if !d.tracker.RecordGateDenied(sig.JobID, now) {
			return nil
		}
	}
	ev := StallEvent{SessionID: sig.SessionID, StallKind: signalToStallKind(sig.Kind), StalledSince: now}
	return d.recordAndAdvance(ctx, ev, epoch)
}

// Poll classifies every tracked session against the idle threshold and,
// for each currently stalled or unknown one, records a StallEvent and
// invokes the escalation ladder, at most once per policy.RungDelay per
// session. A ProgressUnknown session (the event source is unavailable —
// "could not tell") still escalates, fail-closed, but is recorded with
// StallKindUnknown rather than StallKindIdle, so the distinction survives
// into whatever the supervisor-task/human rungs show a person.
//
// A detector built by NewStallDetector refuses to poll while its Run loop
// is not subscribed: a dead subscription means "could not tell", and that
// must not mass-escalate every session.
//
// A per-session escalation outcome (EscalationExhausted, a rung failure)
// never stops the loop from reaching the remaining sessions; Poll
// reports only the first error that is NEITHER of those two expected
// outcomes (matched by sentinel identity, not Kind), after every tracked
// session has been considered.
func (d *Detector) Poll(ctx context.Context) error {
	if d.requireAlive && !d.Alive() {
		return errPollNotAlive
	}
	now := d.clock.Now()
	var firstErr error
	for _, sessionID := range d.tracker.Watched() {
		ev, epoch, ok := d.pollEvent(sessionID, now)
		if !ok {
			continue
		}
		if err := d.recordAndAdvance(ctx, ev, epoch); err != nil && !isExpectedEscalation(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// pollEvent builds sessionID's StallEvent for this Poll, or reports false
// when the session is healthy or its RungDelay has not yet elapsed. It also
// returns the session's episode epoch, read before the tracker status, for
// recordAndAdvance's staleness check.
func (d *Detector) pollEvent(sessionID string, now time.Time) (StallEvent, uint64, bool) {
	epoch := d.episodeEpoch(sessionID)
	status, since := d.tracker.Status(sessionID)
	if status == ProgressHealthy || !d.rungDue(sessionID, now) {
		return StallEvent{}, epoch, false
	}
	if status == ProgressStalled {
		return StallEvent{SessionID: sessionID, StallKind: StallKindIdle, StalledSince: since, ElapsedSeconds: (now.UnixMilli() - since) / 1000}, epoch, true
	}
	return StallEvent{SessionID: sessionID, StallKind: StallKindUnknown}, epoch, true
}

// rungDue reports whether sessionID may be advanced again at now.
func (d *Detector) rungDue(sessionID string, now time.Time) bool {
	if d.rungDelay <= 0 {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	at, ok := d.advanced[sessionID]
	return !ok || now.Sub(at) >= d.rungDelay
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
