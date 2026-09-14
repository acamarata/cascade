package claude

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Purpose (this file): watch the fleet's session stream for this harness's
//
//	processes and report their lifecycle — started, idle, stopped — to the
//	daemon's event bus.
//
// Inputs: a Subscriber (the session stream) and an Emitter (the event bus),
//
//	both injected from internal/plugins because plugins/** may not import
//	internal/** (Art.10.2).
//
// Outputs: one lifecycle event per observed transition, and a synthesized
//
//	stop for every session still believed live when the stream ends.
//
// Constraints: the two error paths this must survive are the ones a daemon
//
//	actually meets. A daemon that is not running is not an error to report
//	once and give up on — Watch retries with backoff until its context ends.
//	A process that VANISHES mid-watch never sends a stop of its own, so the
//	teardown synthesizes one; without that, every downstream consumer keeps
//	believing a dead session is still running.
//
// SPORT: plugins/claude:watch (ADD) — P1-E16-W4-S34-T1.

// SessionState is the state of one observed harness session.
type SessionState string

const (
	// SessionRunning is a live session.
	SessionRunning SessionState = "running"
	// SessionIdle is a session that is alive but doing nothing.
	SessionIdle SessionState = "idle"
	// SessionStopped is a session that has ended.
	SessionStopped SessionState = "stopped"
)

// SessionEvent is one observation from the session stream.
//
// It is this package's OWN type rather than the fleet's record: plugins/**
// may not import internal/**, so the wiring translates. That indirection is
// the boundary working, not overhead.
type SessionEvent struct {
	// ID identifies the session across observations.
	ID string
	// State is what the stream last reported.
	State SessionState
	// Harness names the agent the session belongs to. Only this plugin's
	// own harness is reported on; anything else is ignored.
	Harness string
	// PID is the observed process, when the stream carried one.
	PID int
}

// Lifecycle is the transition reported to the event bus.
type Lifecycle string

const (
	// LifecycleStarted is emitted the first time a session is seen live.
	LifecycleStarted Lifecycle = "session_started"
	// LifecycleIdle is emitted when a live session goes idle.
	LifecycleIdle Lifecycle = "session_idle"
	// LifecycleStopped is emitted when a session ends, including a
	// synthesized stop for one that vanished.
	LifecycleStopped Lifecycle = "session_stopped"
)

// LifecycleEvent is one emitted transition.
type LifecycleEvent struct {
	// Session is the session that changed.
	Session SessionEvent
	// Transition is what happened.
	Transition Lifecycle
	// Synthesized is true when this plugin inferred the transition rather
	// than observing it — today, the stop written for a session still
	// believed live when the stream ended. A consumer that treats an
	// inferred stop as authoritative should know it was inferred.
	Synthesized bool
}

// Subscriber opens the session stream. The returned cancel releases it.
type Subscriber func(ctx context.Context) (<-chan SessionEvent, func(), error)

// Emitter reports one lifecycle transition to the daemon's event bus.
type Emitter func(ctx context.Context, ev LifecycleEvent) error

// HarnessName is the harness whose sessions this plugin reports on.
const HarnessName = "claude"

// Backoff is the retry schedule used when the daemon is unreachable.
//
// It is a fixed, bounded schedule rather than an unbounded exponential: a
// daemon that is down is usually down briefly (a restart) or for a long
// time (not running at all), and in the second case retrying every thirty
// seconds forever is both harmless and what a watcher should do. The
// schedule is a variable so tests can drive it without waiting.
var Backoff = []time.Duration{
	100 * time.Millisecond,
	time.Second,
	5 * time.Second,
	30 * time.Second,
}

// Watcher observes sessions and reports their transitions.
type Watcher struct {
	// Subscribe opens the session stream.
	Subscribe Subscriber
	// Emit reports a transition.
	Emit Emitter
	// Sleep waits between reconnect attempts. Nil means time.Sleep; tests
	// inject a recorder so the backoff is asserted rather than waited out.
	Sleep func(context.Context, time.Duration)

	mu   sync.Mutex
	live map[string]SessionEvent
}

// Run watches until ctx ends.
//
// A subscribe failure is not fatal: it is logged as a retry and the next
// backoff step is waited out, because "the daemon is not up yet" is the
// normal state during boot, not an error worth abandoning the watch over.
// Run returns nil on a clean context cancellation and an error only when it
// cannot continue at all.
func (w *Watcher) Run(ctx context.Context) error {
	if w.Subscribe == nil || w.Emit == nil {
		return fmt.Errorf("cascade-claude: watch: subscriber and emitter must both be wired")
	}
	attempt := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		events, release, err := w.Subscribe(ctx)
		if err != nil {
			if !w.waitBackoff(ctx, attempt) {
				return nil
			}
			attempt++
			continue
		}
		attempt = 0
		w.consume(ctx, events)
		release()
		if ctx.Err() != nil {
			return nil
		}
		// The stream ended without the context being cancelled: the daemon
		// went away mid-watch. Every session still believed live gets a
		// synthesized stop before reconnecting, so no consumer is left
		// holding a session that can never report one itself.
		w.teardown(ctx)
	}
}

// waitBackoff sleeps the step for attempt. It reports false when ctx ended
// during the wait, which is the signal to stop rather than retry.
func (w *Watcher) waitBackoff(ctx context.Context, attempt int) bool {
	step := Backoff[len(Backoff)-1]
	if attempt < len(Backoff) {
		step = Backoff[attempt]
	}
	if w.Sleep != nil {
		w.Sleep(ctx, step)
	} else {
		timer := time.NewTimer(step)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
	}
	return ctx.Err() == nil
}

// consume reads the stream until it closes or ctx ends.
func (w *Watcher) consume(ctx context.Context, events <-chan SessionEvent) {
	for {
		select {
		case <-ctx.Done():
			w.teardown(context.WithoutCancel(ctx))
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			w.observe(ctx, ev)
		}
	}
}

// observe records one event and emits the transition it represents.
//
// Only this plugin's own harness is reported on: the stream carries every
// agent's sessions, and emitting lifecycle events for another harness's
// processes would make this plugin speak for something it does not manage.
func (w *Watcher) observe(ctx context.Context, ev SessionEvent) {
	if ev.Harness != HarnessName || ev.ID == "" {
		return
	}
	w.mu.Lock()
	previous, known := w.live[ev.ID]
	switch ev.State {
	case SessionStopped:
		delete(w.live, ev.ID)
	case SessionRunning, SessionIdle:
		if w.live == nil {
			w.live = map[string]SessionEvent{}
		}
		w.live[ev.ID] = ev
	}
	w.mu.Unlock()

	transition, report := transitionFor(previous, known, ev)
	if !report {
		return
	}
	_ = w.Emit(ctx, LifecycleEvent{Session: ev, Transition: transition})
}

// transitionFor decides what an observation means.
//
// The repeat-suppression is the point: the stream re-reports a session's
// state on every refresh, and emitting "started" each time would turn one
// session into a stream of duplicate lifecycle events downstream.
func transitionFor(previous SessionEvent, known bool, ev SessionEvent) (Lifecycle, bool) {
	switch ev.State {
	case SessionStopped:
		return LifecycleStopped, known
	case SessionIdle:
		return LifecycleIdle, !known || previous.State != SessionIdle
	case SessionRunning:
		return LifecycleStarted, !known
	default:
		// Unreachable today: observe() only forwards the three states
		// above. Reported as "started" rather than dropped so a state
		// added later fails loudly in a test instead of vanishing.
		return LifecycleStarted, !known
	}
}

// teardown synthesizes a stop for every session still believed live.
//
// Sorted by id so the emitted order is deterministic: two runs of the same
// teardown produce the same sequence, which is what makes a test of it
// meaningful and a log of it readable.
func (w *Watcher) teardown(ctx context.Context) {
	w.mu.Lock()
	ids := make([]string, 0, len(w.live))
	for id := range w.live {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	stale := make([]SessionEvent, 0, len(ids))
	for _, id := range ids {
		stale = append(stale, w.live[id])
	}
	w.live = map[string]SessionEvent{}
	w.mu.Unlock()

	for _, session := range stale {
		session.State = SessionStopped
		_ = w.Emit(ctx, LifecycleEvent{
			Session:     session,
			Transition:  LifecycleStopped,
			Synthesized: true,
		})
	}
}
