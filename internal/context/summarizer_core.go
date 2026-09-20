package context

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: Summarizer, the rolling-summary engine (05-PEWS-PLAN-W4-W6.md
//   §Wave 5 §Epic U S-46.T2): GetSummary maintains a stale-checked,
//   single-flight-guarded summary per {entityID, Granularity} pair,
//   persisted through the B/S-02 provider.Store abstraction in the
//   context domain (R-14.62). The SummarizerGetter adapter T1 declares
//   (composer_types.go) lives in summarizer_dispatch.go with the rest of
//   the model-facing half.
// Inputs: a provider.ModelExecutor (the sole model.execute door, K/S-22.T1),
//   a provider.Store, a provider.TokenCounter, and a Clock, all required at
//   construction; an optional SummaryEventPublisher.
// Outputs: SummaryOutcome, or a typed error for caller misuse and real
//   storage read failures -- see summarizer_types.go's SummaryOutcome doc
//   comment for the split.
// Constraints: no ctx in the struct (02 §v1.1); every crossing method takes
//   ctx first. Single-flight is keyed by {entityID, Level, sourceVersion}:
//   two goroutines racing the SAME source version share one regeneration,
//   while a caller carrying NEWER content is never handed the older
//   version's result.
// SPORT: context-engine/summarizer-core (ADD, P1-E21-W5-S46-T2).

// summarizerNamespace is the B/S-02 Store namespace every summary record is
// keyed under: storage.DomainContext (R-14.62 -- summaries live in the
// context domain, matching S-45.T3's storage choice). Referencing the
// typed constant, rather than a bare "context" literal, means this file
// fails to compile rather than silently drifting if the domain is ever
// renamed.
const summarizerNamespace = string(storage.DomainContext)

// Summarizer is the rolling-summary engine. Build one with NewSummarizer;
// the zero value is not usable.
type Summarizer struct {
	executor provider.ModelExecutor
	store    provider.Store
	counter  provider.TokenCounter
	clock    Clock
	events   SummaryEventPublisher

	mu       sync.Mutex
	inflight map[string]*inflightCall
}

// NewSummarizer validates its four required dependencies and returns a
// ready Summarizer. All four are required: a Summarizer that cannot
// dispatch, cannot persist, cannot measure a budget, or cannot timestamp is
// not a partially-usable one. events may be nil -- see
// SummaryEventPublisher, which the daemon composition root injects.
func NewSummarizer(executor provider.ModelExecutor, store provider.Store, counter provider.TokenCounter, clock Clock, events SummaryEventPublisher) (*Summarizer, error) {
	if executor == nil {
		return nil, errSummarizerNilExecutor()
	}
	if store == nil {
		return nil, errSummarizerNilStore()
	}
	if counter == nil {
		return nil, errSummarizerNilCounter()
	}
	if clock == nil {
		return nil, errSummarizerNilClock()
	}
	return &Summarizer{
		executor: executor, store: store, counter: counter, clock: clock,
		events: events, inflight: make(map[string]*inflightCall),
	}, nil
}

// GetSummary returns entityID's rolling summary at level, regenerating it
// via model.execute when the stored record is missing or its SourceVersion
// differs from the caller's current sourceVersion (the caller, not a clock,
// decides staleness -- R-14.62's rolling-window contract). A regeneration
// failure never surfaces as an error here: it degrades to the last valid
// stored record (or the zero Record, if none exists yet) plus the events
// that say so, per this ticket's never-block-the-compose-path hard rule.
func (s *Summarizer) GetSummary(ctx context.Context, entityID string, level Granularity, sourceContent, sourceVersion string) (SummaryOutcome, error) {
	if err := validateGetSummaryInput(ctx, entityID, level); err != nil {
		return SummaryOutcome{}, err
	}
	key := summaryKey(entityID, level)
	existing, found, err := s.loadRecord(ctx, key)
	if err != nil {
		return SummaryOutcome{}, err
	}
	if found && existing.SourceVersion == sourceVersion {
		return SummaryOutcome{Record: existing}, nil
	}
	fresh, regenErr := s.regenerateGuarded(ctx, key, existing, entityID, level, sourceContent, sourceVersion)
	if regenErr == nil {
		return SummaryOutcome{Record: fresh}, nil
	}
	outcome := degradeOnFailure(entityID, level, existing, found, regenErr)
	s.publish(ctx, outcome)
	return outcome, nil
}

// publish reports a degraded outcome's events to the injected publisher.
// A nil publisher drops them: whether anyone is listening must never change
// what the engine returns.
func (s *Summarizer) publish(ctx context.Context, outcome SummaryOutcome) {
	if s.events == nil {
		return
	}
	if outcome.Failed != nil {
		s.events.Publish(ctx, SummaryEvent{Failed: outcome.Failed})
	}
	if outcome.Warning != nil {
		s.events.Publish(ctx, SummaryEvent{Stale: outcome.Warning})
	}
}

// validateGetSummaryInput refuses caller misuse before any dependency is
// consulted: nil ctx, empty entityID, or an out-of-range Granularity.
func validateGetSummaryInput(ctx context.Context, entityID string, level Granularity) error {
	if ctx == nil {
		return errSummarizerNilContext()
	}
	if entityID == "" {
		return errSummarizerEmptyEntityID()
	}
	if !level.Valid() {
		return errSummarizerInvalidGranularity(level)
	}
	return nil
}

// regenerateGuarded runs regenerate behind the single-flight guard and
// persists a successful result before returning it. The guard key carries
// the source version as well as the storage key, so a concurrent caller
// holding newer content starts its own regeneration instead of adopting the
// older one's answer.
func (s *Summarizer) regenerateGuarded(ctx context.Context, key string, prior SummaryRecord, entityID string, level Granularity, content, version string) (SummaryRecord, error) {
	return s.singleflightDo(inflightKey(key, version), func() (SummaryRecord, error) {
		record, err := s.regenerate(ctx, prior, entityID, level, content, version)
		if err != nil {
			return SummaryRecord{}, err
		}
		if err := s.saveRecord(ctx, key, record); err != nil {
			return SummaryRecord{}, err
		}
		return record, nil
	})
}

// regenerate dispatches one model.execute call that FOLDS the new content
// window into prior's summary (the rolling half of "rolling summaries"),
// checks the response against the level's size bound, and wraps the result
// into a SummaryRecord. A prior with empty Content means there is nothing to
// fold into and the dispatch asks for a first summary instead.
func (s *Summarizer) regenerate(ctx context.Context, prior SummaryRecord, entityID string, level Granularity, content, version string) (SummaryRecord, error) {
	req, err := s.buildModelRequest(ctx, entityID, level, prior.Content, content)
	if err != nil {
		return SummaryRecord{}, err
	}
	output, err := dispatchExecute(ctx, s.executor, req)
	if err != nil {
		return SummaryRecord{}, err
	}
	if err := s.checkOutputSize(ctx, level, output); err != nil {
		return SummaryRecord{}, err
	}
	return SummaryRecord{
		EntityID: entityID, Level: level, Content: output,
		SourceVersion: version, GeneratedAt: s.clock.Now(),
	}, nil
}

// summaryKey builds the {entity_id, granularity_level} storage key
// (R-14.62). \x1f (ASCII unit separator) delimits the two halves so an
// entityID containing an ordinary character never collides across levels.
func summaryKey(entityID string, level Granularity) string {
	return entityID + "\x1f" + level.String()
}

// inflightKey extends the storage key with the source version to form the
// single-flight key {entityID, Level, sourceVersion}. Same separator, same
// reason.
func inflightKey(storageKey, sourceVersion string) string {
	return storageKey + "\x1f" + sourceVersion
}

// loadRecord reads key from the store and JSON-decodes it. A missing key
// (KindNotFound) is reported as found=false with a nil error -- storetest's
// own convention -- never as an error.
func (s *Summarizer) loadRecord(ctx context.Context, key string) (SummaryRecord, bool, error) {
	raw, err := s.store.Get(ctx, summarizerNamespace, key)
	if cascade.HasKind(err, cascade.KindNotFound) {
		return SummaryRecord{}, false, nil
	}
	if err != nil {
		return SummaryRecord{}, false, errSummarizerDependency(err, "context: summarizer: reading stored summary")
	}
	var record SummaryRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return SummaryRecord{}, false, cascade.Wrap(cascade.KindIntegrity, err, "context: summarizer: decoding stored summary")
	}
	return record, true, nil
}

// saveRecord JSON-encodes record and writes it to the store under key.
func (s *Summarizer) saveRecord(ctx context.Context, key string, record SummaryRecord) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "context: summarizer: encoding summary for storage")
	}
	if err := s.store.Put(ctx, summarizerNamespace, key, raw); err != nil {
		return errSummarizerDependency(err, "context: summarizer: writing summary to storage")
	}
	return nil
}

// inflightCall tracks one in-progress (or just-completed) regeneration for
// one single-flight key: every concurrent caller sharing the key waits on wg
// and reads the same record/err, so exactly one regeneration ever runs per
// key at a time (this ticket's "no duplicate concurrent regenerations"
// hard rule).
type inflightCall struct {
	wg     sync.WaitGroup
	record SummaryRecord
	err    error
}

// singleflightDo runs fn for key if no call for key is already in flight,
// else waits for that call and shares its result -- the same shape as
// golang.org/x/sync/singleflight.Group.Do, hand-rolled here rather than
// promoting an indirect module dependency to direct for one call site.
func (s *Summarizer) singleflightDo(key string, fn func() (SummaryRecord, error)) (SummaryRecord, error) {
	s.mu.Lock()
	if call, ok := s.inflight[key]; ok {
		s.mu.Unlock()
		call.wg.Wait()
		return call.record, call.err
	}
	call := &inflightCall{}
	call.wg.Add(1)
	s.inflight[key] = call
	s.mu.Unlock()

	call.record, call.err = fn()
	call.wg.Done()

	s.mu.Lock()
	delete(s.inflight, key)
	s.mu.Unlock()

	return call.record, call.err
}
