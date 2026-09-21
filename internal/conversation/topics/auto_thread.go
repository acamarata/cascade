// Purpose: AutoThreader, the pipeline that consumes a Segmenter's
//   []Boundary output and drives ThreadStore to file each resulting turn
//   partition into the correct thread, resolving each partition's topic
//   through a TaxonomyConfig. AutoThreader also carries Reassign, the
//   misfile-correction path (reassign.go), since both operate the same
//   ThreadStore against the same taxonomy.
// Inputs: a Segmenter, a ThreadStore, a TaxonomyConfig, an ExemplarStore, a
//   MisfileEventPublisher and a Clock at construction; a []Turn per Route
//   call.
// Outputs: one ThreadID per topic segment Route files turns into, in
//   segment order; or a typed error - propagated from the Segmenter or the
//   ThreadStore without being swallowed, or raised here for a boundary list
//   that does not describe a partition of the window.
// Constraints: NewAutoThreader takes an already-built Segmenter, never raw
//   model/embedder dependencies, so a test double can stand in without
//   this package's construction machinery (R-14.64, matching
//   segmenter_types.go's own reasoning for declaring Segmenter as an
//   interface). NewDefaultAutoThreader is the production convenience
//   constructor that calls NewSegmenter and wires the result into
//   NewAutoThreader.
//
//   SEGMENT-ZERO HAS NO LABEL. segmenter_types.go's Boundary doc is
//   explicit: "the first turn (index 0) starts the first segment
//   implicitly and is never itself reported as a boundary" - so the
//   window's first segment (turns before the first reported Boundary, or
//   every turn when Segment returns none) carries no classifier Label at
//   all. buildSegments assigns it the empty label "", which
//   TaxonomyConfig.Resolve already documents resolving to Fallback like
//   any other unmapped label (taxonomy.go) - no separate "unclassified"
//   case is invented here.
//
//   RE-DELIVERY IS IDEMPOTENT. Each turn's identity is its content address
//   at its window index (NewTopicTurnID), and ThreadStore.AppendTurn is
//   documented as a no-op for an id already in the thread, so routing the
//   same window twice files N turns, not 2N.
// SPORT: internal/conversation/topics auto-thread (ADD) (P1-E21-W5-S45-T3).

package topics

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// AutoThreader routes a window of turns into topic threads and corrects a
// misfiled turn: Route calls its Segmenter, resolves each resulting
// partition's topic through its TaxonomyConfig, and files the partition's
// turns into the ThreadStore thread that topic selects; Reassign
// (reassign.go) moves one already-filed turn to a different topic.
type AutoThreader struct {
	segmenter Segmenter
	store     ThreadStore
	taxonomy  TaxonomyConfig
	exemplars *ExemplarStore
	publisher MisfileEventPublisher
	clock     Clock
}

// NewAutoThreader validates its required dependencies and returns a ready
// AutoThreader. taxonomy is passed by value (TaxonomyConfig's zero value is
// itself usable - see taxonomy.go) so there is nothing to nil-check on it.
// exemplars, publisher and clock are Reassign's dependencies: an
// AutoThreader that cannot record an exemplar, cannot publish a correction,
// or cannot timestamp one is not a partially-usable one, so all three are
// required rather than optional.
func NewAutoThreader(
	segmenter Segmenter, store ThreadStore, taxonomy TaxonomyConfig,
	exemplars *ExemplarStore, publisher MisfileEventPublisher, clock Clock,
) (*AutoThreader, error) {
	if segmenter == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "topics: NewAutoThreader: Segmenter must not be nil")
	}
	if store == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "topics: NewAutoThreader: ThreadStore must not be nil")
	}
	if exemplars == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "topics: NewAutoThreader: ExemplarStore must not be nil")
	}
	if publisher == nil {
		return nil, cascade.New(cascade.KindInvalidInput,
			"topics: NewAutoThreader: MisfileEventPublisher must not be nil")
	}
	if clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "topics: NewAutoThreader: Clock must not be nil")
	}
	return &AutoThreader{
		segmenter: segmenter, store: store, taxonomy: taxonomy,
		exemplars: exemplars, publisher: publisher, clock: clock,
	}, nil
}

// NewDefaultAutoThreader is the production constructor: it builds a
// Segmenter via NewSegmenter (segmenter_core.go, P1-E21-W5-S45-T2) from
// executor, embedder, and cfg, then wires the result into a new
// AutoThreader alongside the rest. Kept separate from NewAutoThreader so a
// caller wiring a real pipeline never has to construct a Segmenter by hand,
// while a test keeps the narrow, double-friendly constructor.
func NewDefaultAutoThreader(
	executor provider.ModelExecutor, embedder provider.Embedder, cfg HysteresisConfig,
	store ThreadStore, taxonomy TaxonomyConfig,
	exemplars *ExemplarStore, publisher MisfileEventPublisher, clock Clock,
) (*AutoThreader, error) {
	seg, err := NewSegmenter(executor, embedder, cfg)
	if err != nil {
		return nil, err
	}
	return NewAutoThreader(seg, store, taxonomy, exemplars, publisher, clock)
}

// routeSegment is one contiguous run of turns sharing a topic, built from
// the Segmenter's boundary list by buildSegments.
type routeSegment struct {
	start, end int // turns[start:end], end exclusive
	label      string
}

// buildSegments turns boundaries into the contiguous [start,end) partitions
// of turns, prefixing the implicit turn-zero segment this file's header
// comment documents.
//
// Segment's own contract says boundaries are sorted and every TurnIndex is
// >=1, but a Segmenter is an injected dependency, and this function is the
// only thing standing between a boundary list and a slice expression. A
// TurnIndex that is negative, zero, repeated, out of order, or at/past the
// end of the window is therefore refused with a typed KindInvalidInput
// error naming the offending index rather than panicking on the slice or
// silently producing an empty segment (which would create a thread for a
// topic no turn was filed under). The single "does not advance" test below
// catches negative, zero, repeated and decreasing indices at once: each
// boundary must be strictly greater than the previous segment's start,
// which begins at 0.
func buildSegments(turnCount int, boundaries []Boundary) ([]routeSegment, error) {
	segs := make([]routeSegment, 0, len(boundaries)+1)
	start, label := 0, ""
	for n, b := range boundaries {
		if b.TurnIndex <= start {
			return nil, cascade.Newf(cascade.KindInvalidInput,
				"topics: route: boundary %d has TurnIndex %d, which does not advance past the current "+
					"segment's start %d: boundary indices must be strictly increasing and at least 1",
				n, b.TurnIndex, start)
		}
		if b.TurnIndex >= turnCount {
			return nil, cascade.Newf(cascade.KindInvalidInput,
				"topics: route: boundary %d has TurnIndex %d, outside the %d-turn window",
				n, b.TurnIndex, turnCount)
		}
		segs = append(segs, routeSegment{start: start, end: b.TurnIndex, label: label})
		start, label = b.TurnIndex, b.Label
	}
	return append(segs, routeSegment{start: start, end: turnCount, label: label}), nil
}

// Route scans turns for topic boundaries, resolves each resulting
// partition's topic via a.taxonomy, and files the partition's turns into
// the a.store thread that topic selects.
//
// An empty window short-circuits BEFORE the Segmenter is called: there is
// nothing to segment, so spending a classify/embed round trip to be told so
// would be waste, and Segment's own contract already returns no boundaries
// for it. Every dependency failure - the Segmenter's or the ThreadStore's -
// is returned to the caller unmodified; Route never swallows one into a
// partial result.
func (a *AutoThreader) Route(ctx context.Context, turns []Turn) ([]ThreadID, error) {
	if len(turns) == 0 {
		return nil, nil
	}
	boundaries, err := a.segmenter.Segment(ctx, turns)
	if err != nil {
		return nil, err
	}
	segs, err := buildSegments(len(turns), boundaries)
	if err != nil {
		return nil, err
	}
	threadIDs := make([]ThreadID, 0, len(segs))
	for _, seg := range segs {
		threadID, fileErr := a.fileSegment(ctx, turns, seg)
		if fileErr != nil {
			return nil, fileErr
		}
		threadIDs = append(threadIDs, threadID)
	}
	return threadIDs, nil
}

// fileSegment resolves seg's topic, selects its thread, and appends every
// turn in the partition. Each turn's id is computed here, from its own
// window index, which is what makes a re-delivered window idempotent (see
// NewTopicTurnID).
func (a *AutoThreader) fileSegment(ctx context.Context, turns []Turn, seg routeSegment) (ThreadID, error) {
	topicType := a.taxonomy.Resolve(seg.label)
	if err := validateTopicType(topicType); err != nil {
		return "", err
	}
	threadID, err := a.store.CreateOrSelect(ctx, topicType)
	if err != nil {
		return "", err
	}
	for i := seg.start; i < seg.end; i++ {
		turn := ThreadTurn{ID: NewTopicTurnID(threadID, i, turns[i]), Turn: turns[i]}
		if err := a.store.AppendTurn(ctx, threadID, turn); err != nil {
			return "", err
		}
	}
	return threadID, nil
}
