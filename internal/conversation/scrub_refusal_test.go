package conversation

// Purpose: the two refusals that need more than a failing collaborator --
//   a residual secret surviving the rewrite, the real rewriter's own
//   refusal of an untaggable span, and a secret straddling a segment
//   boundary. Split from scrub_error_test.go under Art.10.3's 300-line
//   file cap; TestScrubErrorPath (there) registers these as subtests.
// SPORT: internal.conversation.scrub/ADDED (tests) (P1-E20-W5-S44-T1).

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// unrewritingRewriter is the ONE rewriter double in this package, and it
// exists because the real rewriter cannot produce the state the contract's
// Phase 3 re-scan guards against: it is handed exactly the hits the same
// detector found, so its output never keeps one. This returns the text
// unchanged so the re-scan sees the secret still there -- the only way to
// drive that refusal through the production RPC path.
type unrewritingRewriter struct{}

func (unrewritingRewriter) Rewrite(text []byte, _ []secrets.DetectionHit) (secrets.RewriteResult, error) {
	return secrets.RewriteResult{Text: text}, nil
}

// testScrubResidualMatch drives the residual re-scan through the real RPC
// path: a real detector, quarantine store and broker, with
// unrewritingRewriter in place of the rewriter so its output still carries
// the secret. The turn is refused, nothing is stored, the vault entry the
// completed vault phase wrote is inert (nothing references it), and the
// quarantine entry stays live.
func testScrubResidualMatch(t *testing.T) {
	golden := loadScrubGolden(t, "single_span.golden")
	store := newTestStore(t)
	bus := &fakeBus{}
	_, quarantine, dir := newTestScrubPipeline(t, bus)
	broker, err := secrets.NewBroker(newScrubTestCustody(t, dir), nil)
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("new detector: %v", err)
	}
	pipeline := NewDefaultScrubPipelineFrom(detector, quarantine, broker, unrewritingRewriter{}, bus)

	registry := newScrubAdapter(t, store, bus, pipeline)
	if _, errObj := appendOneSegment(t, registry, "th1", golden.Input); errObj == nil {
		t.Fatal("chat.append_turn succeeded with a rewriter that left the secret in place")
	}
	if turns, _ := store.ListTurns(context.Background(), "th1"); len(turns) != 0 {
		t.Fatalf("turn was committed despite a residual secret: %v", turns)
	}
	if segs, _ := store.ListSegments(context.Background(), NewTurnID("th1", 0, RoleUser)); len(segs) != 0 {
		t.Fatalf("segments were stored despite a residual secret: %v", segs)
	}
	pending, err := quarantine.List()
	if err != nil || len(pending) != 1 {
		t.Fatalf("quarantine.List() = %v, %v, want one live entry", pending, err)
	}
	assertDivergence(t, bus, scrubPhaseRewrite, golden.Canaries)
}

// testScrubRewriteRefusal is the real rewriter's own refusal: a
// corroborated high-entropy span has no tag type (secrets/tags.go's
// TagFor), so Rewrite errors and the turn is refused. Content is
// harvested byte-for-byte from internal/secrets/detector_test.go's
// TestNamedEntropyIsCorroborated -- a real fixture proving a real
// refusal, not an invented string.
func testScrubRewriteRefusal(t *testing.T) {
	const content = "wifi password: 7Kq2mZx9PLw4Rt6VbN3sQe8Hj1Cd5Fg0"
	store := newTestStore(t)
	bus := &fakeBus{}
	pipeline, quarantine, _ := newTestScrubPipeline(t, bus)
	registry := newScrubAdapter(t, store, bus, pipeline)

	if _, errObj := appendOneSegment(t, registry, "th1", content); errObj == nil {
		t.Fatal("chat.append_turn succeeded over an unmapped high-entropy span")
	}
	pending, err := quarantine.List()
	if err != nil || len(pending) != 1 {
		t.Fatalf("quarantine.List() = %v, %v, want one entry (never released on rewrite failure)", pending, err)
	}
	if turns, _ := store.ListTurns(context.Background(), "th1"); len(turns) != 0 {
		t.Fatalf("turn was committed despite the rewrite phase failing: %v", turns)
	}
	assertDivergence(t, bus, scrubPhaseRewrite, []string{content})
}

// testScrubStraddlesSegments is the turn-scope refusal: the golden's own
// secret is split down the middle across two segments of one turn, so
// neither segment carries a detectable span on its own but the turn does.
// Turn-wide detection finds it, no single segment contains it, and the
// turn is refused rather than rewritten across the boundary.
func testScrubStraddlesSegments(t *testing.T) {
	golden := loadScrubGolden(t, "single_span.golden")
	cut := splitInsideCanary(t, golden)
	store := newTestStore(t)
	bus := &fakeBus{}
	pipeline, _, _ := newTestScrubPipeline(t, bus)
	registry := newScrubAdapter(t, store, bus, pipeline)

	_, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		ThreadID: "th1", Role: "user",
		Segments: []appendSegmentWire{
			{Kind: "text", Content: golden.Input[:cut]},
			{Kind: "text", Content: golden.Input[cut:]},
		},
	})
	if errObj == nil {
		t.Fatal("chat.append_turn succeeded with a secret straddling two segments")
	}
	// The refusal must reach the caller AS A POLICY REFUSAL, not as a
	// transient unavailability an operator would retry.
	if want := cascade.NewRPCError(ErrScrubSpanStraddlesSegments).Code; errObj.Code != want {
		t.Fatalf("straddle refusal wire code = %d, want the policy-denied code %d", errObj.Code, want)
	}
	for _, c := range golden.Canaries {
		if strings.Contains(errObj.Message, c) {
			t.Fatal("the straddle refusal quotes the secret in its wire message")
		}
	}
	turns, _ := store.ListTurns(context.Background(), "th1")
	if len(turns) != 0 {
		t.Fatalf("turn was committed despite a straddling secret: %v", turns)
	}
	if segs, _ := store.ListSegments(context.Background(), NewTurnID("th1", 0, RoleUser)); len(segs) != 0 {
		t.Fatalf("segments were stored despite a straddling secret: %v", segs)
	}
	assertDivergence(t, bus, scrubPhaseDetect, golden.Canaries)
}
