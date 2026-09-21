// Package topics (acceptance_errors_test.go): Purpose: the P1-E21-W5-S46-T5
// error-path half of the topic-auto-filing acceptance story
// (12-QUALITY-CONSTITUTION.md Art.3: happy-path-only is a CR-A blocking
// finding). Each subtest drives a REAL shipped unit with the specific
// failing input the ticket names, never a double of the unit under
// acceptance.
// Inputs: a malformed corpus-record fixture; a real Segmenter given an
// empty turn window; a real ObserveLogger given an empty turn window.
// Outputs: none (t.Fatal only).
// Constraints: no network; every assertion names the concrete input that
// triggers it, per LANE-RULES §11.
// SPORT: internal/conversation/topics acceptance (ADD) (P1-E21-W5-S46-T5).
package topics

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestAcceptanceErrorPaths is the checks-list entry point.
func TestAcceptanceErrorPaths(t *testing.T) {
	t.Run("SegmenterMalformedFixture", acceptanceSegmenterMalformedFixture)
	t.Run("SegmenterEmptyFixture", acceptanceSegmenterEmptyFixture)
	t.Run("ObserveLogAbsent", acceptanceObserveLogAbsent)
}

// acceptanceSegmenterMalformedFixture is "Segmenter receives malformed
// fixture -> structured error, no crash": a corpus-record fixture with no
// turns (the shape LoadCorpus feeds Segment from disk) is refused by the
// real loader with a typed KindInvalidInput error naming the defect,
// rather than reaching Segment as a zero-length window that silently
// segments to nothing.
func acceptanceSegmenterMalformedFixture(t *testing.T) {
	malformed := []byte(`{"id":"acceptance-malformed","turns":[],"boundaries":[],"topic_labels":{}}`)
	_, err := LoadCorpusRecord(malformed)
	if err == nil {
		t.Fatalf("LoadCorpusRecord(%s) = nil error, want a structured KindInvalidInput refusal "+
			"(no turns is not a segmentable fixture)", malformed)
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("LoadCorpusRecord(%s) error = %v, want KindInvalidInput", malformed, err)
	}

	unparseable := []byte(`{not valid json`)
	if _, err := LoadCorpusRecord(unparseable); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("LoadCorpusRecord(%s) error = %v, want KindInvalidInput on unparseable JSON",
			unparseable, err)
	}
}

// acceptanceSegmenterEmptyFixture is the "empty fixture" half of the same
// acceptance line, tested against its own real contract rather than an
// invented one: segmenter_types.go's Segmenter.Segment documents "a nil or
// empty turns is not an error: Segment returns (nil, nil)" - so a real
// Segmenter handed an empty window must not crash and must not fabricate a
// boundary, which this test proves against the real engine rather than
// assuming the doc comment.
func acceptanceSegmenterEmptyFixture(t *testing.T) {
	seg, err := newTestSegmenter(keywordClassifier{}, onehotEmbedder{}, HysteresisConfig{Threshold: 0.5, Window: 2})
	if err != nil {
		t.Fatalf("NewSegmenter: %v", err)
	}
	for name, turns := range map[string][]Turn{"nil": nil, "empty slice": {}} {
		bounds, err := seg.Segment(context.Background(), turns)
		if err != nil {
			t.Fatalf("Segment(%s) = %v, want no error (empty window is documented as a no-op)", name, err)
		}
		if bounds != nil {
			t.Fatalf("Segment(%s) boundaries = %+v, want nil: an empty window has nothing to boundary", name, bounds)
		}
	}
}

// acceptanceObserveLogAbsent is "Observe-log absent -> graceful no-op,
// observe-log skipped, pipeline continues": observe_log.go:163's own
// contract ("An empty window is a no-op before either path, and before
// any store read") means Observe with no turns never reaches the
// first-use-at store, the audit publisher, or the AutoThreader - proven
// here by handing it a nil AuditPublisher-backed ObserveLogger that would
// panic on any of those three if the no-op path were bypassed.
func acceptanceObserveLogAbsent(t *testing.T) {
	f := newAcceptanceFixture(t)
	observer, err := NewObserveLogger(f.threader, f.kv, f.clock, panicAuditPublisher{t})
	if err != nil {
		t.Fatalf("NewObserveLogger: %v", err)
	}
	for name, turns := range map[string][]Turn{"nil": nil, "empty slice": {}} {
		result, err := observer.Observe(context.Background(), turns)
		if err != nil {
			t.Fatalf("Observe(%s) = %v, want no error: an absent window is a graceful no-op", name, err)
		}
		if result.Observed || len(result.ThreadIDs) != 0 || len(result.Proposals) != 0 {
			t.Fatalf("Observe(%s) result = %+v, want the zero ObserveResult: the pipeline is skipped "+
				"entirely, not run and discarded", name, result)
		}
	}
}

// panicAuditPublisher fails the test the instant Publish is called,
// proving acceptanceObserveLogAbsent's empty-window calls never reach the
// audit-emit step - a silent pass here would mean the no-op path ran the
// pipeline and threw the result away instead of skipping it, which is
// exactly the "a stage is a gate" anti-pattern this ticket's review brief
// warns against.
type panicAuditPublisher struct{ t *testing.T }

func (p panicAuditPublisher) Publish(
	context.Context, string, events.EventKind, string, []byte,
) (events.Event, error) {
	p.t.Fatalf("AuditPublisher.Publish called on an empty-window Observe: the no-op path must never " +
		"reach the audit step")
	return events.Event{}, nil
}
