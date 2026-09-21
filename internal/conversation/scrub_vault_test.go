package conversation

// Purpose: the vault phase's naming contract and the turn-scope helpers.
//   A scrub must never overwrite an operator's existing vault entry, and
//   two hits in one turn that suggest the same name must become two
//   entries and two distinct tags -- otherwise the second write destroys
//   the first secret and both tags rehydrate to the same value. Also
//   covers scrubSegments' shape guard.
// SPORT: internal.conversation.scrub/ADDED (tests) (P1-E20-W5-S44-T1).

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/secrets"
)

// splitInsideCanary returns a byte offset that falls INSIDE the golden's
// first canary, so splitting the input there puts half the secret in each
// of two segments.
func splitInsideCanary(t *testing.T, g scrubGolden) int {
	t.Helper()
	at := strings.Index(g.Input, g.Canaries[0])
	if at < 0 {
		t.Fatalf("%s: canary not found in its own input", g.Name)
	}
	return at + len(g.Canaries[0])/2
}

// seedVaultEntry writes name=value through a real broker over the same
// custody the pipeline will use -- an operator's pre-existing secret.
func seedVaultEntry(t *testing.T, dir, name, value string) {
	t.Helper()
	broker, err := secrets.NewBroker(newScrubTestCustody(t, dir), nil)
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}
	if _, err := broker.Set(context.Background(), name, []byte(value), secrets.SetUpdate); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
}

// TestScrubVault_NeverOverwritesAnExistingSecret is the no-clobber proof:
// OPENAI_API_KEY already holds an operator's value, a turn arrives whose
// detected secret suggests that same name, and afterwards the operator's
// value is untouched while the turn references a different entry.
func TestScrubVault_NeverOverwritesAnExistingSecret(t *testing.T) {
	golden := loadScrubGolden(t, "single_span.golden")
	store := newTestStore(t)
	bus := &fakeBus{}
	pipeline, _, dir := newTestScrubPipeline(t, bus)
	seedVaultEntry(t, dir, "OPENAI_API_KEY", "old")
	registry := newScrubAdapter(t, store, bus, pipeline)

	result, errObj := appendOneSegment(t, registry, "th1", golden.Input)
	if errObj != nil {
		t.Fatalf("chat.append_turn: %+v", errObj)
	}
	segs, err := store.ListSegments(context.Background(), result.(appendTurnResult).TurnID)
	if err != nil || len(segs) != 1 {
		t.Fatalf("ListSegments = %v, %v", segs, err)
	}

	if got := string(verifyVaulted(t, dir, "OPENAI_API_KEY")); got != "old" {
		t.Fatalf("the operator's OPENAI_API_KEY = %q after a scrub, want %q -- the scrub overwrote it", got, "old")
	}
	if strings.Contains(segs[0].Content, golden.ExpectedTag) {
		t.Fatalf("stored segment = %q; it references the operator's existing entry rather than its own", segs[0].Content)
	}
	if !strings.Contains(segs[0].Content, "<apikey>OPENAI_API_KEY_2</apikey>") {
		t.Fatalf("stored segment = %q, want a tag naming the entry this turn's secret landed in", segs[0].Content)
	}
	if got := string(verifyVaulted(t, dir, "OPENAI_API_KEY_2")); got != golden.Canaries[0] {
		t.Fatalf("OPENAI_API_KEY_2 = %q, want this turn's secret", got)
	}
	assertNoCanary(t, "stored segment", segs[0].Content, golden.Canaries)
}

// TestScrubVault_TwoHitsOneSuggestedName proves two secrets in one turn
// that the detector names identically become two vault entries and two
// distinct tags, each rehydrating to ITS OWN span.
//
// Both values are the corpus canary's own shape: the second is the first
// with its body counter changed, so this stays a corpus-derived fixture
// rather than an invented credential (see testdata/README.md).
func TestScrubVault_TwoHitsOneSuggestedName(t *testing.T) {
	golden := loadScrubGolden(t, "single_span.golden")
	first := golden.Canaries[0]
	second := strings.Replace(first, "Canary0000", "Canary5555", 1)
	if second == first {
		t.Fatalf("deriving a second canary from %q changed nothing", first)
	}
	content := "staging OPENAI_API_KEY=" + first + " and backup OPENAI_API_KEY=" + second + " end.\n"

	store := newTestStore(t)
	bus := &fakeBus{}
	pipeline, _, dir := newTestScrubPipeline(t, bus)
	registry := newScrubAdapter(t, store, bus, pipeline)

	result, errObj := appendOneSegment(t, registry, "th1", content)
	if errObj != nil {
		t.Fatalf("chat.append_turn: %+v", errObj)
	}
	segs, err := store.ListSegments(context.Background(), result.(appendTurnResult).TurnID)
	if err != nil || len(segs) != 1 {
		t.Fatalf("ListSegments = %v, %v", segs, err)
	}
	stored := segs[0].Content
	assertNoCanary(t, "stored segment", stored, []string{first, second})

	firstTag := "<apikey>OPENAI_API_KEY</apikey>"
	secondTag := "<apikey>OPENAI_API_KEY_2</apikey>"
	if !strings.Contains(stored, firstTag) || !strings.Contains(stored, secondTag) {
		t.Fatalf("stored segment = %q, want both %s and %s -- two names for two secrets", stored, firstTag, secondTag)
	}
	if strings.Index(stored, firstTag) > strings.Index(stored, secondTag) {
		t.Fatalf("stored segment = %q: the tags are in the wrong order for their spans", stored)
	}
	if got := string(verifyVaulted(t, dir, "OPENAI_API_KEY")); got != first {
		t.Fatalf("OPENAI_API_KEY = %q, want the first span's value", got)
	}
	if got := string(verifyVaulted(t, dir, "OPENAI_API_KEY_2")); got != second {
		t.Fatalf("OPENAI_API_KEY_2 = %q, want the second span's value", got)
	}
}

// TestScrubVault_NameFollowsTurnContextAcrossASegmentBoundary records a
// real consequence of turn-scoped detection, rather than leaving it to be
// discovered later: the detector derives a hit's suggested name from the
// characters immediately before it, and those characters are the TURN's,
// not the segment's. A preceding segment that ends mid-word therefore
// changes the vault name this turn's secret lands under.
//
// That is a naming effect, not a leak, and the invariants that matter still
// hold: the stored segment carries no canary, and the name in its tag is
// the name the value is actually stored under -- so the tag still resolves.
// The alternative would be joining segments with invented separator bytes,
// which can hide a credential split across a boundary; scrub_scope.go
// documents why that trade is refused.
func TestScrubVault_NameFollowsTurnContextAcrossASegmentBoundary(t *testing.T) {
	golden := loadScrubGolden(t, "single_span.golden")
	store := newTestStore(t)
	bus := &fakeBus{}
	pipeline, _, dir := newTestScrubPipeline(t, bus)
	registry := newScrubAdapter(t, store, bus, pipeline)

	// The first segment ends mid-word ("...in it"), and the second starts
	// with the variable name, so the joined turn reads "itOPENAI_API_KEY=".
	result, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		ThreadID: "th1", Role: "user",
		Segments: []appendSegmentWire{
			{Kind: "text", Content: "nothing sensitive in it"},
			{Kind: "text", Content: "OPENAI_API_KEY=" + golden.Canaries[0] + "\n"},
		},
	})
	if errObj != nil {
		t.Fatalf("chat.append_turn: %+v", errObj)
	}
	segs, err := store.ListSegments(context.Background(), result.(appendTurnResult).TurnID)
	if err != nil || len(segs) != 2 {
		t.Fatalf("ListSegments = %v, %v", segs, err)
	}
	assertNoCanary(t, "stored segment", segs[1].Content, golden.Canaries)

	name := tagNameIn(t, segs[1].Content)
	if name == "OPENAI_API_KEY" {
		t.Skip("the detector no longer carries turn context across the boundary; nothing left to record")
	}
	if !strings.HasSuffix(name, "OPENAI_API_KEY") {
		t.Fatalf("tag name = %q, want the variable name with the preceding turn text on it", name)
	}
	if got := string(verifyVaulted(t, dir, name)); got != golden.Canaries[0] {
		t.Fatalf("the tag names %q, but that entry holds %q -- the tag does not resolve", name, got)
	}
}

// tagNameIn returns the NAME inside the single <apikey> tag in content.
func tagNameIn(t *testing.T, content string) string {
	t.Helper()
	const openTag, closeTag = "<apikey>", "</apikey>"
	start := strings.Index(content, openTag)
	end := strings.Index(content, closeTag)
	if start < 0 || end < start {
		t.Fatalf("content carries no <apikey> tag: %q", content)
	}
	return content[start+len(openTag) : end]
}

// truncatingPipeline returns one fewer segment than it was given -- the
// injected-seam misbehaviour scrubSegments' shape guard exists for. It is
// a double for the INTERFACE, not for any real component.
type truncatingPipeline struct{}

func (truncatingPipeline) ScrubTurn(_ context.Context, segs []ScrubSegment) ([][]byte, error) {
	return contentsOf(segs[:len(segs)-1]), nil
}

// TestScrubSegments_RefusesAChangedSegmentCount proves the adapter refuses
// a pipeline whose output does not line up with its input rather than
// storing a turn with a segment's content shifted onto its neighbour.
func TestScrubSegments_RefusesAChangedSegmentCount(t *testing.T) {
	store := newTestStore(t)
	bus := &fakeBus{}
	registry := newScrubAdapter(t, store, bus, truncatingPipeline{})

	_, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		ThreadID: "th1", Role: "user",
		Segments: []appendSegmentWire{{Kind: "text", Content: "one"}, {Kind: "text", Content: "two"}},
	})
	if errObj == nil {
		t.Fatal("chat.append_turn succeeded over a pipeline that dropped a segment")
	}
	if turns, _ := store.ListTurns(context.Background(), "th1"); len(turns) != 0 {
		t.Fatalf("turn was committed despite a segment-count mismatch: %v", turns)
	}
}

// TestSplitHitsBySegment_EmptySegmentClaimsNothing pins containingSegment's
// zero-length guard: a turn whose first segment is empty must map its hit
// onto the segment that actually holds the bytes.
func TestSplitHitsBySegment_EmptySegmentClaimsNothing(t *testing.T) {
	golden := loadScrubGolden(t, "single_span.golden")
	segs := []ScrubSegment{{Ref: "a", Content: nil}, {Ref: "b", Content: []byte(golden.Input)}}
	joined, bounds := joinSegments(segs)
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("new detector: %v", err)
	}
	hits := detector.ScanCertain(joined)
	if len(hits) == 0 {
		t.Fatal("the golden fixture produced no certain hits")
	}
	perSegment, err := splitHitsBySegment(hits, bounds)
	if err != nil {
		t.Fatalf("splitHitsBySegment: %v", err)
	}
	if len(perSegment[0]) != 0 {
		t.Fatalf("the empty segment claimed %d hits", len(perSegment[0]))
	}
	if len(perSegment[1]) != len(hits) {
		t.Fatalf("segment 1 got %d of %d hits", len(perSegment[1]), len(hits))
	}
	if perSegment[1][0].Offset != hits[0].Offset {
		t.Fatalf("rebased offset = %d, want %d for a segment starting at 0", perSegment[1][0].Offset, hits[0].Offset)
	}
	if ref := turnRef(segs); ref != "a" {
		t.Fatalf("turnRef = %q, want the first segment's ref", ref)
	}
	// A turn with no segments at all: turnRef must answer rather than
	// index an empty slice, because it is called from the refusal path.
	if ref := turnRef(nil); ref != "" {
		t.Fatalf("turnRef(nil) = %q, want the empty ref", ref)
	}
}
