// Purpose: AwayController — the away-mode state machine (Active ->
//
//	AwayPending -> Away -> Active) that decides when the operator is
//	absent, gates the S-49.T1 notification queues' accumulation buffer
//	accordingly, and journals every Away-entry and Active-return via
//	M/S-27.T1's entity journal so a kill -9 mid-away is reconstructable
//	on resume (ReplayState + NewAwayControllerFrom, away_config.go).
//
// Inputs: an injected PresenceSource (the operator-activity side), an
//
//	AdmissionIdleSource (M/S-26.T2's machine-idle side), an injected
//	StallSource (R/S-39.T5 stall notices), periodic Tick calls against
//	the injected clock, and pushed ActivityEvents via HandleEvent.
//
// Outputs: SetAccumulate(true/false) on the router; journal.Store.Append
//
//	(KindIntent on Away-entry, KindAck on Active-return, sharing one
//	away-episode operation id, in that order by construction); on
//	Active-return, a DigestCompiler.CompileDrained call over the items
//	drained inside that transition (digest.go).
//
// Constraints:
//   - The state transition, the accumulate flag flip and — on the return
//     edge — the buffer drain happen inside ONE critical section, together
//     with the journal append for that edge. A concurrent Tick and
//     HandleEvent therefore cannot interleave into state=Active with
//     accumulation still on (a permanent notification blackhole), cannot
//     journal KindAck before its own KindIntent, and cannot make a returned
//     episode's digest refusable by re-entering Away.
//   - "Sustained idle" means both signals held for the WHOLE threshold
//     window: any admission-busy sample restarts the window.
//   - The auto-advance tier-1 ceiling (R/S-39.T2) is never touched here:
//     this file has no import of, and no effect on, that ceiling.
//
// SIGNAL SOURCES (recorded, decided, not guessed): the contract names
// supervision.Subscribe, event Kind "attention.idle",
// AdmissionController.IdleState() and a "supervision.stalled" payload
// carrying the stalled notification's id. None of the four exists in this
// tree, and the nearest real signals are different facts, not faithful
// substitutes — Inflight==0 && QueueDepth==0 is "the governor has no
// work", i.e. machine idleness, and a session-change event is neither
// necessary nor sufficient for operator presence. Presenting either as
// presence would be a fail-open guess. This controller therefore consumes
// PresenceSource and StallSource (away_config.go): narrow injected
// interfaces a composition root implements against whatever the tree
// really publishes. The wiring ticket that implements them is the same one
// that constructs this controller at all; see the deferral recorded in
// internal/build/testonly-allow.json, which states plainly that no P1
// ticket owns away-mode wiring today.
//
// SPORT: internal.notify.AwayController/ADDED (P1-E23-W5-S49-T2).

package notify

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/fleet/journal"
)

// AwayController is the Active/AwayPending/Away state machine. The zero
// value is not usable; construct with NewAwayController or, after a
// restart, NewAwayControllerFrom.
type AwayController struct {
	deps AwayDeps
	cfg  AwayConfig
	log  *slog.Logger

	mu           sync.Mutex
	state        AwayState
	lastActivity time.Time
	lastBusy     time.Time
	episodeID    string
	episodeSeq   uint64
}

// NewAwayController builds a controller starting fresh in StateActive as of
// deps.Clock.Now(). It is NewAwayControllerFrom with an empty
// ReplayResult; a process that has a journal to replay must use
// NewAwayControllerFrom instead, or it silently restarts every away
// episode as Active and never writes the matching KindAck.
func NewAwayController(deps AwayDeps, cfg AwayConfig) *AwayController {
	return NewAwayControllerFrom(deps, cfg, ReplayResult{State: StateActive})
}

// NewAwayControllerFrom builds a controller and installs restored, the
// state ReplayState reconstructed from the journal. A restored StateAway
// resumes accumulation immediately (before the first Tick, so a
// notification arriving during startup is accumulated rather than
// dispatched into an absent operator's face) and keeps the pre-restart
// episode id, so the eventual Active-return writes its KindAck against the
// episode the pre-restart KindIntent opened. A restored StateAway with no
// episode id is refused and downgraded to Active with a WARN: resuming an
// anonymous episode would accumulate forever with no episode to close.
func NewAwayControllerFrom(deps AwayDeps, cfg AwayConfig, restored ReplayResult) *AwayController {
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	c := &AwayController{
		deps: deps, cfg: cfg, log: log,
		state: StateActive, lastActivity: deps.Clock.Now(),
	}
	c.install(restored)
	return c
}

// install applies restored to a freshly-constructed controller. A restored
// non-Active state backdates lastActivity by the whole threshold so the
// first Tick evaluates the resumed state instead of immediately reverting
// it to Active on a zero elapsed-idle reading.
func (c *AwayController) install(restored ReplayResult) {
	switch restored.State {
	case StateAway:
		if restored.EpisodeID == "" {
			c.log.Warn("notify: refusing to resume Away with no episode id, starting Active")
			return
		}
		c.state = StateAway
		c.episodeID = restored.EpisodeID
		c.lastActivity = c.lastActivity.Add(-c.cfg.Threshold)
		c.deps.Router.SetAccumulate(true)
	case StateAwayPending:
		c.state = StateAwayPending
		c.lastActivity = c.lastActivity.Add(-c.cfg.Threshold)
	case StateActive:
		// Nothing to install: the constructor's own initial state.
	}
}

// State returns the controller's current state.
func (c *AwayController) State() AwayState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// EpisodeID returns the open away-episode id, or "" when not Away. It is
// what a restart's KindAck must be written against, and what a caller
// compares a resumed episode to.
func (c *AwayController) EpisodeID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.episodeID
}

// Snapshot returns the controller's state and whether accumulation is on as
// ONE consistent fact, read under the same lock every transition holds. A
// status surface must use this rather than State() and Accumulating()
// separately: read apart, the two can be sampled either side of a transition
// and report a combination the machine never actually occupied. Under this
// package's own invariant the pair is always consistent — Away implies
// accumulating, anything else implies not accumulating — so an inconsistent
// return means the flag was flipped outside the critical section.
func (c *AwayController) Snapshot() (AwayState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state, c.deps.Router.Accumulating()
}

// Tick is the periodic driver: it drains pending stall notices, reads
// PresenceSource, and advances the state machine against the injected
// clock. It returns the resulting state.
func (c *AwayController) Tick(ctx context.Context) AwayState {
	c.drainStalls()
	now := c.deps.Clock.Now()

	c.mu.Lock()
	ended := ""
	var drained []Notification
	if at := c.presenceActivity(); at.After(c.lastActivity) {
		ended, drained = c.observeActivityLocked(ctx, at)
	} else {
		c.advanceLocked(ctx, now)
	}
	state := c.state
	c.mu.Unlock()

	c.compileDigest(ctx, ended, drained)
	return state
}

// HandleEvent feeds one pushed operator-activity observation: it resets the
// idle window and, on the Away->Active edge, journals the Active-return and
// compiles the return digest. It returns the resulting state.
func (c *AwayController) HandleEvent(ctx context.Context, ev ActivityEvent) AwayState {
	c.drainStalls()
	at := ev.At
	if at.IsZero() {
		at = c.deps.Clock.Now()
	}

	c.mu.Lock()
	ended, drained := c.observeActivityLocked(ctx, at)
	state := c.state
	c.mu.Unlock()

	c.compileDigest(ctx, ended, drained)
	return state
}

// observeActivityLocked records activity at and returns to Active. It
// returns the episode id that just closed, or "" when no Away episode was
// open, together with the episode's accumulated items. Caller holds c.mu;
// the accumulate flip, the buffer DRAIN and the KindAck append all happen
// here, inside that one critical section.
//
// The drain belongs in here, not in the digest compiler: read after the
// unlock, a concurrent Tick pair that re-enters Away turns the gate back on,
// and a Compile re-reading the live gate then refuses a digest for an episode
// that has ALREADY returned (AC#4 fails, items join the next episode). The
// snapshot returned here is this episode's own set, and compileDigest
// delivers from it whatever the machine has done since.
func (c *AwayController) observeActivityLocked(ctx context.Context, at time.Time) (string, []Notification) {
	if at.After(c.lastActivity) {
		c.lastActivity = at
	}
	prev := c.state
	episodeID := c.episodeID
	c.state = StateActive
	c.episodeID = ""
	if prev != StateAway {
		return "", nil
	}
	c.deps.Router.SetAccumulate(false)
	drained := c.deps.Router.DrainAccumulated()
	c.appendJournal(ctx, journal.KindAck, episodeID, awayJournalPayload{EpisodeID: episodeID, ReturnedAt: at})
	return episodeID, drained
}

// advanceLocked evaluates one Tick's elapsed-idle reading. Caller holds
// c.mu. An admission-busy sample restarts the both-signals-idle window, so
// Away requires the window to have held continuously for the whole
// threshold — an instantaneous idle reading at one Tick is not "sustained".
func (c *AwayController) advanceLocked(ctx context.Context, now time.Time) {
	busy := c.admissionBusy()
	if busy {
		c.lastBusy = now
	}
	if c.state == StateAway {
		return // only operator activity leaves Away, never machine idleness.
	}
	if now.Sub(c.lastActivity) < c.cfg.Threshold {
		c.state = StateActive
		return
	}
	if c.state == StateActive {
		c.state = StateAwayPending
		return
	}
	if busy || now.Sub(c.windowStart()) < c.cfg.Threshold {
		return // still AwayPending: the confirming window has not held.
	}
	c.enterAwayLocked(ctx, now)
}

// windowStart returns the instant the current both-signals-idle window
// began: the later of the last operator activity and the last
// admission-busy sample. Caller holds c.mu.
func (c *AwayController) windowStart() time.Time {
	if c.lastBusy.After(c.lastActivity) {
		return c.lastBusy
	}
	return c.lastActivity
}

// enterAwayLocked opens an away episode: it mints the episode id, moves to
// StateAway, turns accumulation on and journals the KindIntent — all inside
// the caller's critical section, so KindIntent is ordered before any
// KindAck for this episode and accumulation is never on outside StateAway.
func (c *AwayController) enterAwayLocked(ctx context.Context, now time.Time) {
	c.episodeSeq++
	c.episodeID = fmt.Sprintf("%s-away-%d-%d", c.deps.EntityID, now.UnixNano(), c.episodeSeq)
	c.state = StateAway
	c.deps.Router.SetAccumulate(true)
	c.appendJournal(ctx, journal.KindIntent, c.episodeID, awayJournalPayload{EpisodeID: c.episodeID, EnteredAt: now})
}
