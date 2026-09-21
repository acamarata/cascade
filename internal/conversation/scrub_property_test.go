package conversation

// Purpose: the SSE non-leak property and the A-T3 hot-path bench budget.
//   The property varies where the secret sits and how many segments the
//   turn has, and asserts three things per case rather than only the
//   absence of a canary: exactly one event was published, it carries the
//   vault tag, and it carries no canary. "Contains no X" alone passes on
//   an empty payload, which is why the positive assertion is there.
// SPORT: internal.conversation.scrub/ADDED (tests) (P1-E20-W5-S44-T1).

import (
	"context"
	"encoding/json"
	"testing"
)

// scrubLeakCase is one generated turn: the segments to send, and the
// golden whose canaries and expected tag apply to it.
type scrubLeakCase struct {
	name     string
	golden   scrubGolden
	segments []string
	secretAt int
}

// scrubLeakCases builds the property's input space: each golden placed at
// every position of turns with one, two and three segments. The filler
// around it is ordinary prose, so the only detectable span in each turn is
// the fixture's own -- and moving that span changes both its byte offset
// inside the turn and which segment must carry the tag.
//
// The filler ENDS IN A SENTENCE BREAK on purpose. Detection is turn-scoped
// over the concatenated segments (no separator bytes, scrub_scope.go), and
// the detector derives a hit's suggested name from the characters
// immediately before it -- so filler ending mid-word would join onto the
// next segment's variable name and change the name the vault entry lands
// under. That behaviour is real and is pinned by
// TestScrubVault_NameFollowsTurnContextAcrossASegmentBoundary; this
// property is about leakage, and keeping the names stable is what lets it
// assert the fixture's own expected output at every position.
func scrubLeakCases(t *testing.T) []scrubLeakCase {
	t.Helper()
	const filler = "some ordinary chat text with nothing sensitive in it.\n"
	var cases []scrubLeakCase
	for _, name := range []string{"single_span.golden", "multi_span.golden"} {
		golden := loadScrubGolden(t, name)
		for count := 1; count <= 3; count++ {
			for at := 0; at < count; at++ {
				segs := make([]string, count)
				for i := range segs {
					segs[i] = filler
				}
				segs[at] = golden.Input
				cases = append(cases, scrubLeakCase{
					name:     name + "/" + itoa(int64(count)) + "segments/at" + itoa(int64(at)),
					golden:   golden,
					segments: segs,
					secretAt: at,
				})
			}
		}
	}
	return cases
}

// TestScrubSSELeakProperty is the contract's property test: over every
// generated turn, the SSE mirror publishes exactly one event, that event
// carries the vault-reference tag for the secret the turn contained, and
// it carries none of the fixture's canaries. `-count=10` (this ticket's
// checks entry) re-runs the whole space 10 times under -race.
func TestScrubSSELeakProperty(t *testing.T) {
	for _, tc := range scrubLeakCases(t) {
		t.Run(tc.name, func(t *testing.T) { assertNoSecretReachesTheMirror(t, tc) })
	}
}

// assertNoSecretReachesTheMirror runs one generated turn through the real
// RPC path and asserts the whole property for it.
func assertNoSecretReachesTheMirror(t *testing.T, tc scrubLeakCase) {
	t.Helper()
	store := newTestStore(t)
	bus := &fakeBus{}
	// A fresh vault per case: the tag a case expects is the detector's
	// suggested name, which is only what the broker returns when that name
	// is free.
	pipeline, _, _ := newTestScrubPipeline(t, bus)
	registry := newScrubAdapter(t, store, bus, pipeline)

	wire := make([]appendSegmentWire, len(tc.segments))
	for i, content := range tc.segments {
		wire[i] = appendSegmentWire{Kind: "text", Content: content}
	}
	result, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		ThreadID: "th1", Role: "user", Segments: wire,
	})
	if errObj != nil {
		t.Fatalf("chat.append_turn errored: %+v", errObj)
	}

	if len(bus.events) != 1 {
		t.Fatalf("published %d events, want exactly 1 (the turn_appended echo)", len(bus.events))
	}
	echo := bus.events[0]
	if echo.Namespace != turnAppendedNamespace || echo.Kind != turnAppendedKind {
		t.Fatalf("echo published on %s/%s, want %s/%s",
			echo.Namespace, echo.Kind, turnAppendedNamespace, turnAppendedKind)
	}
	// The payload is JSON, so a tag's angle brackets are escaped in the raw
	// bytes: decode it and assert on the content the wire actually carries
	// rather than on an escaped substring.
	var echoed turnAppendedPayload
	if err := json.Unmarshal(echo.Payload, &echoed); err != nil {
		t.Fatalf("SSE payload does not decode: %v", err)
	}
	if len(echoed.Segments) != len(tc.segments) {
		t.Fatalf("SSE payload carries %d segments, want %d", len(echoed.Segments), len(tc.segments))
	}
	assertGoldenOutput(t, tc.golden, echoed.Segments[tc.secretAt].Content)
	assertNoCanary(t, tc.name+" SSE payload", string(echo.Payload), tc.golden.Canaries)

	segs, err := store.ListSegments(context.Background(), result.(appendTurnResult).TurnID)
	if err != nil || len(segs) != len(tc.segments) {
		t.Fatalf("ListSegments = %v, %v, want %d segments", segs, err, len(tc.segments))
	}
	assertGoldenOutput(t, tc.golden, segs[tc.secretAt].Content)
}

// BenchmarkScrubPipeline_1000TurnCorpus is the A-T3 hot-path bench budget
// baseline this ticket's task 8 requires: b.N iterations cycling a
// 1,000-entry corpus, 10% carrying single_span.golden's real secret, over
// the real pipeline.
func BenchmarkScrubPipeline_1000TurnCorpus(b *testing.B) {
	golden := loadScrubGolden(b, "single_span.golden")
	const clean = "ordinary chat content with nothing sensitive in it at all"
	corpus := make([][]ScrubSegment, 1000)
	for i := range corpus {
		content := clean
		if i%10 == 0 {
			content = golden.Input
		}
		corpus[i] = []ScrubSegment{{Ref: "bench-seg", Content: []byte(content)}}
	}
	bus := &fakeBus{}
	pipeline, _, _ := newTestScrubPipeline(b, bus)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := pipeline.ScrubTurn(ctx, corpus[i%len(corpus)]); err != nil {
			b.Fatalf("scrub: %v", err)
		}
	}
}
