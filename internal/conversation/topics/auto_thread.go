// Purpose: AutoThreader, the pipeline that consumes a Segmenter's
//   []Boundary output and drives ThreadStore to file each resulting turn
//   partition into the correct thread, resolving each partition's topic
//   through a TaxonomyConfig. AutoThreader also carries Reassign, the
//   misfile-correction path (reassign.go), since both operate the same
//   ThreadStore against the same taxonomy.
// Inputs: a Segmenter, a Classifier, a ThreadStore, a TaxonomyConfig, an
//   ExemplarStore, a MisfileEventPublisher and a Clock at construction; a
//   []Turn per Route call.
// Outputs: one ThreadID per topic segment Route files turns into, in
//   segment order; or a typed error - propagated from the Segmenter, the
//   Classifier or the ThreadStore without being swallowed, or raised here
//   for a boundary list that does not describe a partition of the window.
// Constraints: NewAutoThreader takes an already-built Segmenter and
//   Classifier, never raw model/embedder dependencies, so a test double can
//   stand in without this package's construction machinery (R-14.64,
//   matching segmenter_types.go's own reasoning for declaring Segmenter as
//   an interface). NewDefaultAutoThreader is the production convenience
//   constructor - see ONE SHARED CLASSIFIER below.
//
//   ONE SHARED CLASSIFIER, NOT TWO (P1-E21-W5-S46-T5 D6, 2026-09-21; fixes
//   the confirming review's C2 finding). Before this fix, NewSegmenter and
//   NewDefaultAutoThreader each built their own NewClassifier over the same
//   executor, so turn 0 was classified TWICE per Route window - no
//   guarantee ModelExecutor.Execute is deterministic, so the labels could
//   diverge, and every window paid twice. NewSegmenterWith
//   (segmenter_core.go) now takes an already-built Classifier, so
//   NewDefaultAutoThreader builds ONE and hands it to both; its bounded
//   memo cache (segmenter_types.go) makes classifyOpener's identical
//   turn-0 lookup a cache hit, not a second dispatch.
//
//   SEGMENT-ZERO IS CLASSIFIED HERE (P1-E21-W5-S46-T5 D5 fix, 2026-09-21).
//   segmenter_types.go's Boundary doc is explicit: turn 0 starts the first
//   segment implicitly and is never itself reported as a boundary, so the
//   window's first segment carries no classifier Label from the Segmenter
//   (buildSegments still seeds it ""). Leaving it there was the defect this
//   acceptance ticket exists to surface: every opener - real, classifiable
//   content - filed under TaxonomyConfig's fallback, because Resolve maps
//   "" to Fallback like any other unmapped label (taxonomy.go). plan's
//   classifyOpener now labels that segment with a.classifier's
//   classification of the window's first turn, the IDENTICAL cheap-lane
//   dispatch segmenterImpl.classifyAll uses for every other turn, so the
//   opener resolves through the taxonomy on its own real content. A
//   classifier abstention (label still "") passes through unchanged: it
//   still falls back honestly rather than inventing a topic for it.
//
//   RE-DELIVERY IS IDEMPOTENT. Each turn's identity is its content address
//   at its window index (NewTopicTurnID), and ThreadStore.AppendTurn is
//   documented as a no-op for an id already in the thread, so routing the
//   same window twice files N turns, not 2N.
//
//   ONE ROUTING DERIVATION. plan below is the pipeline's single routing
//   decision - segment, classify the opener, partition, resolve, validate -
//   shared, not mirrored: Route calls it and files each planned segment,
//   while ObserveLogger.Observe (observe_log.go) calls the SAME method and
//   only reports what it returned. Nothing may re-derive any of those
//   steps; observe_pin_test.go fails if the two ever disagree.
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
	segmenter  Segmenter
	classifier Classifier
	store      ThreadStore
	taxonomy   TaxonomyConfig
	exemplars  *ExemplarStore
	publisher  MisfileEventPublisher
	clock      Clock
}

// NewAutoThreader validates its required dependencies and returns a ready
// AutoThreader. taxonomy is passed by value (TaxonomyConfig's zero value is
// itself usable - see taxonomy.go) so there is nothing to nil-check on it.
// classifier labels the window's implicit opening segment (this file's
// header, SEGMENT-ZERO IS CLASSIFIED HERE); a caller wiring a real pipeline
// passes the SAME instance it hands to NewSegmenterWith (this file's
// header, ONE SHARED CLASSIFIER) - NewDefaultAutoThreader below is that
// wiring for production. A test double may pass any Classifier.
// exemplars, publisher and clock are Reassign's dependencies: an
// AutoThreader that cannot record an exemplar, cannot publish a correction,
// or cannot timestamp one is not a partially-usable one, so all three are
// required rather than optional.
func NewAutoThreader(
	segmenter Segmenter, classifier Classifier, store ThreadStore, taxonomy TaxonomyConfig,
	exemplars *ExemplarStore, publisher MisfileEventPublisher, clock Clock,
) (*AutoThreader, error) {
	if segmenter == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "topics: NewAutoThreader: Segmenter must not be nil")
	}
	if classifier == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "topics: NewAutoThreader: Classifier must not be nil")
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
		segmenter: segmenter, classifier: classifier, store: store, taxonomy: taxonomy,
		exemplars: exemplars, publisher: publisher, clock: clock,
	}, nil
}

// NewDefaultAutoThreader (P1-E21-W5-S46-T5 D6) builds exactly ONE
// Classifier via NewClassifier(executor) and hands that SAME instance to
// NewSegmenterWith and NewAutoThreader - see ONE SHARED CLASSIFIER above.
// Kept separate from NewAutoThreader so a real-pipeline caller never
// constructs a Segmenter or Classifier by hand.
func NewDefaultAutoThreader(
	executor provider.ModelExecutor, embedder provider.Embedder, cfg HysteresisConfig,
	store ThreadStore, taxonomy TaxonomyConfig,
	exemplars *ExemplarStore, publisher MisfileEventPublisher, clock Clock,
) (*AutoThreader, error) {
	// NewClassifier(nil) returns a non-nil Classifier wrapping a nil
	// executor, invisible to NewSegmenterWith's own nil check - refuse here.
	if executor == nil {
		return nil, cascade.New(cascade.KindInvalidInput,
			"topics: NewDefaultAutoThreader: ModelExecutor must not be nil")
	}
	classifier := NewClassifier(executor)
	seg, err := NewSegmenterWith(classifier, embedder, cfg)
	if err != nil {
		return nil, err
	}
	return NewAutoThreader(seg, classifier, store, taxonomy, exemplars, publisher, clock)
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

// plannedSegment is one partition of a planned route: the routeSegment
// buildSegments derived, plus the TopicType its label resolved to, already
// validated. plan produces these; fileSegment consumes one.
type plannedSegment struct {
	seg       routeSegment
	topicType TopicType
}

// plan is the routing decision itself, with nothing applied: segment the
// window, classify its unlabeled opener, partition, resolve each
// partition's topic through a.taxonomy, and refuse a resolved topic that
// could not be a safe key. It touches the Segmenter, the Classifier and the
// taxonomy only - never a.store - so a caller that must not mutate
// anything (ObserveLogger.Observe) can run the real decision.
//
// The boundary list comes back alongside the plan because observe mode's
// event reports the boundaries themselves, not only the segments they
// partition the window into. Every dependency error is returned unmodified.
func (a *AutoThreader) plan(ctx context.Context, turns []Turn) ([]plannedSegment, []Boundary, error) {
	boundaries, err := a.segmenter.Segment(ctx, turns)
	if err != nil {
		return nil, nil, err
	}
	segs, err := buildSegments(len(turns), boundaries)
	if err != nil {
		return nil, nil, err
	}
	if err := a.classifyOpener(ctx, turns, segs); err != nil {
		return nil, nil, err
	}
	planned := make([]plannedSegment, 0, len(segs))
	for _, seg := range segs {
		topicType := a.taxonomy.Resolve(seg.label)
		if err := validateTopicType(topicType); err != nil {
			return nil, nil, err
		}
		planned = append(planned, plannedSegment{seg: seg, topicType: topicType})
	}
	return planned, boundaries, nil
}

// classifyOpener labels segs' implicit opening segment (buildSegments'
// start,label:=0,"" seed - this file's header, SEGMENT-ZERO IS CLASSIFIED
// HERE) with a.classifier's classification of the window's first turn, the
// IDENTICAL dispatch segmenterImpl.classifyAll uses for every other turn
// (segmenter_core.go). An empty turns window has nothing to classify -
// plan's own caller either already returned before reaching here (Route) or
// never calls plan for one (ObserveLogger.Observe) - so this is a defensive
// guard, not a reachable production path. A classifier abstention (label
// "") is written back unchanged: TaxonomyConfig.Resolve already treats ""
// as any other unmapped label, so an abstained opener still resolves to
// Fallback exactly as before this fix.
func (a *AutoThreader) classifyOpener(ctx context.Context, turns []Turn, segs []routeSegment) error {
	if len(turns) == 0 || len(segs) == 0 {
		return nil
	}
	label, err := a.classifier.Classify(ctx, turns[0])
	if err != nil {
		return err
	}
	segs[0].label = label
	return nil
}

// Route plans the window (plan above) and files each planned partition's
// turns into the a.store thread its topic selects.
//
// An empty window short-circuits BEFORE the Segmenter is called: there is
// nothing to segment, so spending a classify/embed round trip to be told so
// would be waste, and Segment's own contract already returns no boundaries
// for it. Every dependency failure - the Segmenter's, the Classifier's or
// the ThreadStore's - is returned to the caller unmodified; Route never
// swallows one into a partial result.
func (a *AutoThreader) Route(ctx context.Context, turns []Turn) ([]ThreadID, error) {
	if len(turns) == 0 {
		return nil, nil
	}
	planned, _, err := a.plan(ctx, turns)
	if err != nil {
		return nil, err
	}
	threadIDs := make([]ThreadID, 0, len(planned))
	for _, p := range planned {
		threadID, fileErr := a.fileSegment(ctx, turns, p)
		if fileErr != nil {
			return nil, fileErr
		}
		threadIDs = append(threadIDs, threadID)
	}
	return threadIDs, nil
}

// fileSegment selects the thread p's already-resolved topic owns and
// appends every turn in the partition. Each turn's id is computed here,
// from its own window index, which is what makes a re-delivered window
// idempotent (see NewTopicTurnID).
func (a *AutoThreader) fileSegment(ctx context.Context, turns []Turn, p plannedSegment) (ThreadID, error) {
	threadID, err := a.store.CreateOrSelect(ctx, p.topicType)
	if err != nil {
		return "", err
	}
	for i := p.seg.start; i < p.seg.end; i++ {
		turn := ThreadTurn{ID: NewTopicTurnID(threadID, i, turns[i]), Turn: turns[i]}
		if err := a.store.AppendTurn(ctx, threadID, turn); err != nil {
			return "", err
		}
	}
	return threadID, nil
}
