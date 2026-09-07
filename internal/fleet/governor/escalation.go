// Package governor (escalation.go) implements EscalationLadder: the
// four-rung escalation path (Retry -> Context -> SupervisorTask -> Human)
// a stuck entity walks when its confidence falls below threshold
// (P1-E13-W3-S27-T3, 04-PEWS-PLAN-W1-W3.md §Epic M S-27.T3).
//
// TERMINATION. Every Advance call does at most one rung's worth of work
// and returns. The rung a call operates on is decided entirely by
// safeRung, whose only fixed point is RungHuman (escalation_types.go), so
// no sequence of Advance calls can cycle back to an earlier rung or
// invent a fifth one: the ladder is monotonic (a rung number this
// EscalationLadder ever assigns never decreases across calls for the same
// entity) and bounded (RungHuman is reached in at most three advances from
// RungRetry). Once the journal shows a RungHuman event for an entity,
// every subsequent Advance for that entity returns EscalationExhausted
// immediately, without calling any seam again — this is what makes the
// Human rung's side effect fire at most once per escalation rather than
// once per retry, and is what stops the ladder from ever looping forever
// on an entity that never recovers.
//
// ATTEMPT BUDGETS (R-21.216). A rung's seam may be called repeatedly
// across Advance calls while confidence stays below threshold, but never
// unboundedly: EscalationPolicy.MaxAttempts caps it, and Advance enforces
// the cap itself before doing any work, independently of whether the seam
// keeps succeeding. A seam that always returns nil therefore still moves
// the ladder forward once its budget is spent (TestEscalationLadderAdvancesOnAttemptExhaustion).
//
// CONCURRENCY. Advance's read-decide-execute-append sequence is not
// atomic at the journal layer by itself (two goroutines could both Replay
// the same last event before either Appends its successor), so this file
// serializes Advance per entity with its own lock (mirrors
// journal.SQLiteStore.lockFor), independent of and in addition to the
// JournalStore's own per-entity write serialization. This is what makes
// "the human rung's side effect fires exactly once" and "attempt counts
// never double-count" hold under concurrent Advance calls for the same
// entity, not merely under sequential ones.
//
// SPORT: internal/fleet/governor.EscalationLadder (ADD, per T-3
//
//	sport_updates).
package governor

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// EscalationLadder advances a stuck entity through the four-rung
// escalation path, journaling every transition. The zero value is not
// usable; construct with NewEscalationLadder.
type EscalationLadder struct {
	journal    journal.Store
	confidence ConfidenceProvider
	retryer    Retryer
	enricher   ContextEnricher
	supervisor SupervisorCreator
	notifier   HumanNotifier
	policy     EscalationPolicy
	clock      runtime.Clock

	entityLocks sync.Map // map[string]*sync.Mutex, one per entity ID
}

// NewEscalationLadder builds an EscalationLadder that reads and writes
// entity rung history through j and calls the five seams to do each
// rung's work. clk is used for every EscalationEvent.Timestamp; a nil clk
// falls back to runtime.NewSystemClock() — tests should always inject a
// frozen clock instead (Art.7.3).
func NewEscalationLadder(j journal.Store, confidence ConfidenceProvider, retryer Retryer, enricher ContextEnricher, supervisor SupervisorCreator, notifier HumanNotifier, policy EscalationPolicy, clk runtime.Clock) *EscalationLadder {
	if clk == nil {
		clk = runtime.NewSystemClock()
	}
	return &EscalationLadder{
		journal:    j,
		confidence: confidence,
		retryer:    retryer,
		enricher:   enricher,
		supervisor: supervisor,
		notifier:   notifier,
		policy:     policy,
		clock:      clk,
	}
}

// Advance is the ladder's single entry point: it reads entityID's current
// rung from its journal history, checks whether entityID is still stuck,
// and if so executes exactly one rung's worth of work before returning.
//
// Returns nil when entityID is not stuck (confidence at or above
// threshold) or when the executed rung's seam succeeded and its attempt
// budget is not yet spent. Returns a typed ErrEscalationRungFailed error
// when a non-terminal rung's seam failed (the ladder has already advanced
// to the next rung by the time this returns). Returns EscalationExhausted
// once RungHuman has been reached and either just failed or was already
// recorded by an earlier call. Returns ErrEscalationJournalUnavailable
// (without having advanced anything) if the journal itself could not be
// read or written.
func (l *EscalationLadder) Advance(ctx context.Context, entityID string) error {
	if entityID == "" {
		return ErrEscalationInvalidInput
	}
	lock := l.lockFor(entityID)
	lock.Lock()
	defer lock.Unlock()

	last, err := l.lastEvent(ctx, entityID)
	if err != nil {
		return err
	}

	rung := RungRetry
	attempt := 0
	if last != nil {
		rung = safeRung(last.Rung)
		attempt = last.Attempt
	}

	belowThreshold, err := l.isBelowThreshold(ctx, entityID)
	if err != nil {
		return err
	}
	if !belowThreshold {
		return nil
	}

	// Terminal guard: once RungHuman's seam has been executed at least
	// once for this entity (Attempt >= 1, whether that attempt succeeded
	// or failed), nothing escalates further and Notify is never called
	// again — this is the ladder's exactly-once guarantee for the human
	// rung's side effect (see this file's package doc). An Attempt of 0
	// at RungHuman means the ladder has just arrived there (via failure
	// or attempt-exhaustion at RungSupervisorTask) but has not yet run
	// Human's own seam, so this guard must not fire until it has.
	if rung == RungHuman && attempt >= 1 {
		return EscalationExhausted
	}

	// R-21.216 attempt-budget pre-check: a rung whose budget the PRIOR
	// calls already spent advances now, before this call does any work at
	// the old rung, regardless of how that rung's seam has been behaving.
	if attempt >= l.policy.MaxAttempts[rung] {
		rung = safeRung(rung + 1)
		attempt = 0
	}
	attempt++

	seamErr := l.executeRung(ctx, rung, entityID)
	if seamErr != nil {
		return l.recordFailure(ctx, entityID, rung, attempt, seamErr)
	}
	return l.appendEvent(ctx, EscalationEvent{
		EntityID:  entityID,
		Rung:      rung,
		Attempt:   attempt,
		Timestamp: l.clock.Now(),
	})
}

// isBelowThreshold reports whether entityID should be treated as stuck. A
// ConfidenceProvider error is fail-closed: it is treated as below
// threshold (escalate) rather than propagated or silently skipped.
func (l *EscalationLadder) isBelowThreshold(ctx context.Context, entityID string) (bool, error) {
	confidence, err := l.confidence.Confidence(ctx, entityID)
	if err != nil {
		return true, nil
	}
	return confidence < l.policy.ConfidenceThreshold, nil
}

// recordFailure appends the failure event for a seam that just failed at
// rung on its attempt'th try. RungHuman has nowhere further to advance
// to, so its failure event stays recorded at RungHuman with attempt
// preserved (the terminal guard above reads this back as "already
// executed, do not retry") and Advance returns EscalationExhausted. Every
// other rung's failure event advances to safeRung(rung+1) with Attempt
// reset to 0 (no attempt has yet been made at the new rung), and Advance
// returns a typed ErrEscalationRungFailed.
func (l *EscalationLadder) recordFailure(ctx context.Context, entityID string, rung EscalationRung, attempt int, seamErr error) error {
	if rung == RungHuman {
		if err := l.appendEvent(ctx, EscalationEvent{
			EntityID:  entityID,
			Rung:      RungHuman,
			Attempt:   attempt,
			LastError: seamErr.Error(),
			Timestamp: l.clock.Now(),
		}); err != nil {
			return err
		}
		return EscalationExhausted
	}
	if err := l.appendEvent(ctx, EscalationEvent{
		EntityID:  entityID,
		Rung:      safeRung(rung + 1),
		Attempt:   0,
		LastError: seamErr.Error(),
		Timestamp: l.clock.Now(),
	}); err != nil {
		return err
	}
	return cascade.Wrapf(cascade.KindUnavailable, ErrEscalationRungFailed, "escalation: rung %s failed for %s: %v", rung, entityID, seamErr)
}

// executeRung calls rung's seam. Every EscalationRung safeRung can ever
// return is listed explicitly (the exhaustive lint analyzer enforces
// this); the default case is unreachable in practice since every rung
// value Advance passes in has already gone through safeRung, but keeps
// this function itself fail-closed if that ever stops being true.
func (l *EscalationLadder) executeRung(ctx context.Context, rung EscalationRung, entityID string) error {
	switch rung {
	case RungRetry:
		return l.retryer.Retry(ctx, entityID)
	case RungContext:
		_, err := l.enricher.Enrich(ctx, entityID)
		return err
	case RungSupervisorTask:
		_, err := l.supervisor.CreateSupervisor(ctx, entityID)
		return err
	case RungHuman:
		return l.notifier.Notify(ctx, EscalationEvent{EntityID: entityID, Rung: RungHuman, Timestamp: l.clock.Now()})
	default:
		return cascade.Wrapf(cascade.KindInternal, ErrEscalationRungFailed, "escalation: unrecognised rung %d", rung)
	}
}

// lastEvent returns entityID's most recently journaled EscalationEvent, or
// nil if none has ever been recorded. The kinds filter is mandatory
// (R-21.216): the same entity_id's log also carries resume cursors
// (S-27.T2) and node-streamed records (Q/S-37.T2), and an unfiltered "last
// entry" read would mis-report the rung.
func (l *EscalationLadder) lastEvent(ctx context.Context, entityID string) (*EscalationEvent, error) {
	entries, err := l.journal.Replay(ctx, entityID, journal.Cursor{}, []journal.Kind{journal.KindEscalation})
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, ErrEscalationJournalUnavailable, "escalation: reading rung history for %s: %v", entityID, err)
	}
	if len(entries) == 0 {
		return nil, nil
	}
	last := entries[len(entries)-1]
	var event EscalationEvent
	if err := json.Unmarshal(last.Payload, &event); err != nil {
		return nil, cascade.Wrapf(cascade.KindIntegrity, ErrEscalationJournalUnavailable, "escalation: decoding rung history for %s: %v", entityID, err)
	}
	return &event, nil
}

// appendEvent journals event under journal.KindEscalation with a freshly
// minted operation id, so that two events for the same entity are never
// mistaken for a retry of one another by Replay's operation-id
// deduplication (replay.go).
func (l *EscalationLadder) appendEvent(ctx context.Context, event EscalationEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "escalation: encoding event")
	}
	opID, err := cascade.NewID()
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "escalation: minting operation id")
	}
	if _, err := l.journal.Append(ctx, event.EntityID, journal.KindEscalation, opID.String(), payload); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, ErrEscalationJournalUnavailable, "escalation: appending event for %s: %v", event.EntityID, err)
	}
	return nil
}

// lockFor returns the per-entity mutex Advance serializes through, so two
// concurrent Advance calls for the same entity cannot both read the same
// last event and each independently decide to advance (which would either
// double-execute a rung's seam or silently drop one caller's transition).
// Different entities never contend for the same lock.
func (l *EscalationLadder) lockFor(entityID string) *sync.Mutex {
	v, _ := l.entityLocks.LoadOrStore(entityID, &sync.Mutex{})
	return v.(*sync.Mutex)
}
