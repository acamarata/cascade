// Purpose: ObserveLogger, the 7-day observe-log gate around AutoThreader:
//   for the first week after the topic engine's first real use, Observe
//   emits a typed audit event describing what the pipeline WOULD have done
//   instead of calling Route, so an uncalibrated segmenter never
//   restructures a new user's threads before it has warmed up. The
//   first-use window's own persistence lives in observe_state.go.
// Inputs: an already-built *AutoThreader (S-45.T3), a B/S-02 provider.Store
//   handle (no KV path, R-16.63), a Clock (package-local, exemplar_store.go),
//   and an AuditPublisher at construction; a []Turn per Observe call.
// Outputs: an ObserveResult - Observed false plus Route's own ThreadIDs in
//   apply mode, Observed true plus one Proposal per planned segment in
//   observe mode - or a typed error from any dependency, unmodified.
// Constraints:
//
//   NO PRODUCTION CALLER YET: nothing in this tree composes a running
//   AutoThreader, so nothing can construct a running ObserveLogger either.
//   NewObserveLogger carries a testonly-allow.json exemption whose reason
//   states that honestly: the per-turn composition root for the topic
//   engine is unowned, S-46.T4's contract builds read-only CLI handlers
//   over the conversation-service RPC and constructs neither type, and the
//   entry is parked pending the planner's disposition of the S-45.T3 and
//   S-45.T4 PCIs. It is NOT licensed by R-14.283, which says the opposite.
//
//   PROPOSING NEVER MUTATES, AND NEVER RE-DERIVES. Observe mode runs the
//   pipeline's single shared planning step, AutoThreader.plan
//   (auto_thread.go), the same method Route runs - it does not mirror
//   segmentation, partitioning, taxonomy resolution or validation here.
//   The proposed thread id is then READ from the same ThreadStore Route
//   would write, through LookupThread: an existing thread yields its real
//   id, and a topic with no thread yet yields the wouldCreatePrefix marker
//   rather than a fabricated id, because ThreadID is opaque to this package
//   (thread_store.go) and only the implementation knows what CreateOrSelect
//   would mint. A lookup failure propagates exactly as CreateOrSelect's
//   does in apply mode - observe mode never reports a routing success the
//   pipeline would not have had. observe_pin_test.go holds the two paths
//   together.
//
//   AUDIT EVENT MECHANISM matches reassign.go's precedent: audit.Kind is a
//   closed 14-member enum with no topic-engine fit, so this file mints its
//   own events.EventKind ("topics.observe_log") into storage.DomainAudit.
//
//   SPEC_REFS DRIFT (LANE-RULES §1, full quote in the journal): this
//   ticket's R-21.228 citation misdescribes that ruling; the ruling that
//   governs the state row is R-16.63's addendum. Filed as a planning PCI.
// SPORT: internal/conversation/topics observe-logger (ADD) (P1-E21-W5-S45-T4).

package topics

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// observeAudit*/observeLogEventName are the audit publish coordinates
// (header, AUDIT EVENT MECHANISM). wouldCreatePrefix marks a proposal for a
// topic that owns no thread yet (header, PROPOSING NEVER MUTATES): a
// marker, deliberately not a guessed ThreadID.
const (
	observeAuditNamespace = string(storage.DomainAudit)
	observeEventSource    = "internal/conversation/topics"
	observeLogEventName   = "topic_observe"
	wouldCreatePrefix     = "would_create:"
)

// EventKindTopicObserve is the open-vocabulary events.EventKind this file
// publishes for an observe-mode decision (header, AUDIT EVENT MECHANISM).
const EventKindTopicObserve events.EventKind = "topics.observe_log"

// AuditPublisher is Observe's publish seam, shaped exactly like
// internal/events.Bus.Publish (reassign.go's MisfileEventPublisher's same
// zero-adapter posture), so a real *events.Bus satisfies both untouched.
type AuditPublisher interface {
	Publish(ctx context.Context, namespace string, kind events.EventKind, source string, payload []byte) (events.Event, error)
}

// Proposal is one planned segment's routing proposal in observe mode.
type Proposal struct {
	// TopicType is the topic the segment's label resolved to, through the
	// same TaxonomyConfig apply mode would have used.
	TopicType TopicType
	// ThreadID is the real id of the thread that already owns TopicType
	// when Existing is true, and the wouldCreatePrefix marker naming
	// TopicType when it is false.
	ThreadID ThreadID
	// Existing reports whether a thread for TopicType was already there.
	// False means apply mode would have created one.
	Existing bool
}

// ObserveResult is one Observe call's outcome. Observed separates the three
// cases a bare ([]ThreadID, error) pair could not: observed with no
// proposals (Observed true, empty Proposals), applied with no threads
// (Observed false, empty ThreadIDs), and an empty window (both empty).
type ObserveResult struct {
	// Observed is true iff this call ran in observe mode: an audit event
	// was published and no conversation thread was touched.
	Observed bool
	// ThreadIDs is Route's own return value, set in apply mode only.
	ThreadIDs []ThreadID
	// Proposals is one entry per planned segment, set in observe mode only.
	Proposals []Proposal
}

// proposedBoundary is one proposed_boundaries entry: a Boundary's TurnIndex
// paired with its TAXONOMY-RESOLVED TopicType (not the raw classifier
// label), matching the plan step's own resolution.
type proposedBoundary struct {
	TurnIndex int       `json:"turn_index"`
	TopicType TopicType `json:"topic_type"`
}

// observeLogEvent is EventKindTopicObserve's JSON payload (contract
// schema). Carries no turn content: TopicType is a caller-configured label
// and every ProposedThreadIDs entry is either a store-issued id or a
// marker derived from one such label, so this payload never widens
// conversation-text exposure (NewTopicTurnID/MisfileEvent's PRIVACY rule).
type observeLogEvent struct {
	Event              string             `json:"event"`
	ProposedBoundaries []proposedBoundary `json:"proposed_boundaries"`
	ProposedThreadIDs  []ThreadID         `json:"proposed_thread_ids"`
	ObservedAt         string             `json:"observed_at"` // RFC3339
	ElapsedDays        float64            `json:"elapsed_days"`
}

// ObserveLogger wraps an AutoThreader with the 7-day observe-log gate.
// Build one with NewObserveLogger; the zero value is not usable.
type ObserveLogger struct {
	threader *AutoThreader
	store    provider.Store
	clock    Clock
	audit    AuditPublisher

	mu         sync.Mutex
	firstUseAt *time.Time
}

// NewObserveLogger validates its four required dependencies and returns a
// ready ObserveLogger.
func NewObserveLogger(threader *AutoThreader, store provider.Store, clock Clock, audit AuditPublisher) (*ObserveLogger, error) {
	switch {
	case threader == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "topics: NewObserveLogger: AutoThreader must not be nil")
	case store == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "topics: NewObserveLogger: Store must not be nil")
	case clock == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "topics: NewObserveLogger: Clock must not be nil")
	case audit == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "topics: NewObserveLogger: AuditPublisher must not be nil")
	}
	return &ObserveLogger{threader: threader, store: store, clock: clock, audit: audit}, nil
}

// Observe delegates to Route in apply mode (value/error unmodified) or
// publishes a typed audit event and returns the proposals it reported in
// observe mode, never touching the ThreadStore's write side. An empty
// window is a no-op before either path, and before any store read.
func (o *ObserveLogger) Observe(ctx context.Context, turns []Turn) (ObserveResult, error) {
	if ctx == nil {
		return ObserveResult{}, cascade.New(cascade.KindInvalidInput, "topics: Observe: ctx must not be nil")
	}
	if len(turns) == 0 {
		return ObserveResult{}, nil
	}
	firstUseAt, err := o.resolveFirstUseAt(ctx)
	if err != nil {
		return ObserveResult{}, err
	}
	now := o.clock.Now()
	if now.Sub(firstUseAt) >= observeWindow {
		threadIDs, routeErr := o.threader.Route(ctx, turns)
		if routeErr != nil {
			return ObserveResult{}, routeErr
		}
		return ObserveResult{ThreadIDs: threadIDs}, nil
	}
	proposals, boundaries, err := o.proposeRouting(ctx, turns)
	if err != nil {
		return ObserveResult{}, err
	}
	if err := o.emitObserveEvent(ctx, proposals, boundaries, firstUseAt, now); err != nil {
		return ObserveResult{}, err
	}
	return ObserveResult{Observed: true, Proposals: proposals}, nil
}

// proposeRouting runs the pipeline's shared planning step and asks the SAME
// ThreadStore, read-only, which thread each planned topic already owns
// (header, PROPOSING NEVER MUTATES).
func (o *ObserveLogger) proposeRouting(ctx context.Context, turns []Turn) ([]Proposal, []Boundary, error) {
	planned, boundaries, err := o.threader.plan(ctx, turns)
	if err != nil {
		return nil, nil, err
	}
	proposals := make([]Proposal, 0, len(planned))
	for _, p := range planned {
		threadID, found, lookupErr := o.threader.store.LookupThread(ctx, p.topicType)
		if lookupErr != nil {
			return nil, nil, lookupErr
		}
		if !found {
			threadID = ThreadID(wouldCreatePrefix + string(p.topicType))
		}
		proposals = append(proposals, Proposal{TopicType: p.topicType, ThreadID: threadID, Existing: found})
	}
	return proposals, boundaries, nil
}

// emitObserveEvent publishes the proposed routing decision as
// EventKindTopicObserve. A publish failure is the call's failure: the event
// is observe mode's only product, so it is returned, never swallowed.
func (o *ObserveLogger) emitObserveEvent(
	ctx context.Context, proposals []Proposal, boundaries []Boundary, firstUseAt, now time.Time,
) error {
	payload, err := json.Marshal(observeLogEvent{
		Event:              observeLogEventName,
		ProposedBoundaries: resolveProposedBoundaries(boundaries, o.threader.taxonomy),
		ProposedThreadIDs:  proposalThreadIDs(proposals),
		ObservedAt:         now.UTC().Format(time.RFC3339),
		ElapsedDays:        now.Sub(firstUseAt).Hours() / 24,
	})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "topics: observe log: encoding observe event")
	}
	_, err = o.audit.Publish(ctx, observeAuditNamespace, EventKindTopicObserve, observeEventSource, payload)
	return err
}

// proposalThreadIDs is the event's proposed_thread_ids projection: one id
// per proposal, in planned-segment order.
func proposalThreadIDs(proposals []Proposal) []ThreadID {
	out := make([]ThreadID, len(proposals))
	for i, p := range proposals {
		out[i] = p.ThreadID
	}
	return out
}

// resolveProposedBoundaries resolves each Boundary's raw Label through
// taxonomy exactly as the plan step does for a segment's label.
func resolveProposedBoundaries(boundaries []Boundary, taxonomy TaxonomyConfig) []proposedBoundary {
	out := make([]proposedBoundary, len(boundaries))
	for i, b := range boundaries {
		out[i] = proposedBoundary{TurnIndex: b.TurnIndex, TopicType: taxonomy.Resolve(b.Label)}
	}
	return out
}
