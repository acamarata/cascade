package secrets

// Purpose: the rewrite engine's tests. Three properties carry the weight:
//
//	the output matches the golden bytes exactly, no canary survives into
//	the output or into any diagnostic, and a second pass changes nothing.
//
// Constraints: canary assertions cover raw, hex and base64 forms, because
//
//	"the secret is not in the output" is only true if it is not in the
//	output in any encoding a formatter could have produced.

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestRewriterGoldens is the fixture table: exact bytes, re-derived
// provenance, canary absence, and a repeated call.
func TestRewriterGoldens(t *testing.T) {
	detector, err := NewDetector(DefaultRegistry(), DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("building the detector: %v", err)
	}
	rewriter := NewRewriter()
	for _, fixture := range loadGoldenFixtures(t) {
		t.Run(fixture.Name, func(t *testing.T) {
			if fixture.Provenance == "detector" {
				checkFixtureProvenance(t, detector, fixture)
			}
			result, rerr := rewriter.Rewrite([]byte(fixture.Input), fixture.Hits)
			if rerr != nil {
				t.Fatalf("Rewrite: %v", rerr)
			}
			if string(result.Text) != fixture.ExpectedOutput {
				t.Fatalf("Rewrite = %q, want %q", string(result.Text), fixture.ExpectedOutput)
			}
			if result.Tainted {
				t.Fatal("a fully rewritten turn is still marked tainted")
			}
			assertNoRewriteCanary(t, fixture, result, nil)
			again, aerr := rewriter.Rewrite([]byte(fixture.Input), fixture.Hits)
			if aerr != nil || !bytes.Equal(again.Text, result.Text) {
				t.Fatalf("a second identical call produced %q, %v", string(again.Text), aerr)
			}
		})
	}
}

// TestRewriteErrorPaths asserts every refusal returns no text, stays
// tainted, and carries the taxonomy's invalid-input kind.
func TestRewriteErrorPaths(t *testing.T) {
	text, hit := singleHit()
	beyond := hit
	beyond.Offset = len(text) + 10
	negative := hit
	negative.Offset = -1
	zeroLen := hit
	zeroLen.Len = 0
	badName := hit
	badName.SuggestedName = "openai-api-key"
	entropy := hit
	entropy.Class = ClassHighEntropy
	cases := map[string]struct {
		text []byte
		hits []DetectionHit
	}{
		"offset beyond text":  {[]byte(text), []DetectionHit{beyond}},
		"negative offset":     {[]byte(text), []DetectionHit{negative}},
		"zero length":         {[]byte(text), []DetectionHit{zeroLen}},
		"unusable name":       {[]byte(text), []DetectionHit{badName}},
		"unmapped class":      {[]byte(text), []DetectionHit{entropy}},
		"hit into empty text": {nil, []DetectionHit{hit}},
		"non-utf8 text":       {[]byte("key=\xff\xfe rest"), nil},
		"bisected rune":       {[]byte("価格は秘密です"), []DetectionHit{{Class: ClassAPIKey, Offset: 1, Len: 5, SuggestedName: "A_KEY"}}},
		"straddles a tag":     {[]byte("head <apikey>OPENAI_API_KEY</apikey> tail"), []DetectionHit{{Class: ClassAPIKey, Offset: 2, Len: 10, SuggestedName: "A_KEY"}}},
	}
	for name, tc := range cases {
		result, err := NewRewriter().Rewrite(tc.text, tc.hits)
		if err == nil {
			t.Fatalf("%s: Rewrite returned %q with no error", name, string(result.Text))
		}
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s: error kind = %v, want invalid input", name, err)
		}
		if result.Text != nil || result.Replacements != nil || !result.Tainted {
			t.Fatalf("%s: a refused rewrite returned %+v", name, result)
		}
		if strings.Contains(err.Error(), "sk-Canary") || strings.Contains(err.Error(), "Canary") {
			t.Fatalf("%s: the error message quotes span bytes: %v", name, err)
		}
	}
}

// TestRewriteNilAndEmptyInputs covers the benign edges: nothing to do is
// not an error, and a nil hit slice is the same as an empty one.
func TestRewriteNilAndEmptyInputs(t *testing.T) {
	rewriter := NewRewriter()
	for name, text := range map[string][]byte{"nil": nil, "empty": {}} {
		result, err := rewriter.Rewrite(text, nil)
		if err != nil {
			t.Fatalf("%s text: %v", name, err)
		}
		if len(result.Text) != 0 || result.Text == nil || result.Tainted {
			t.Fatalf("%s text: got %+v", name, result)
		}
	}
	text, _ := singleHit()
	result, err := rewriter.Rewrite([]byte(text), nil)
	if err != nil || string(result.Text) != text {
		t.Fatalf("a turn with no hits was altered: %q, %v", string(result.Text), err)
	}
}

// TestRewriteIdempotentOnTaggedSpans asserts hits pointing inside an
// existing tag are suppressed: a second pass must not wrap a tag in a tag.
func TestRewriteIdempotentOnTaggedSpans(t *testing.T) {
	rewriter := NewRewriter()
	text, hit := singleHit()
	first, err := rewriter.Rewrite([]byte(text), []DetectionHit{hit})
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	inside := DetectionHit{Class: ClassAPIKey, Pattern: "vendor-api-key-prefix",
		Offset: bytes.Index(first.Text, []byte("OPENAI_API_KEY</apikey>")), Len: len("OPENAI_API_KEY"),
		Confidence: ConfidenceCertain, SuggestedName: "OPENAI_API_KEY"}
	second, err := rewriter.Rewrite(first.Text, []DetectionHit{inside})
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if !bytes.Equal(second.Text, first.Text) {
		t.Fatalf("the second pass changed the turn: %q -> %q", string(first.Text), string(second.Text))
	}
	if len(second.Replacements) != 0 {
		t.Fatalf("the second pass reported replacements: %+v", second.Replacements)
	}
	if second.Tainted {
		t.Fatal("an already-tagged turn came back tainted")
	}
}

// TestRewriterTaintPropagation pins R-21.211: the taint bit clears only
// when every detected span ended up behind a tag.
func TestRewriterTaintPropagation(t *testing.T) {
	text, hit := singleHit()
	rewriter := NewRewriter()
	clean, err := rewriter.Rewrite([]byte(text), []DetectionHit{hit})
	if err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	if clean.Tainted {
		t.Fatal("a turn with every span tagged is still tainted")
	}
	if bytes.Contains(clean.Text, []byte("Canary")) {
		t.Fatalf("the rewritten turn still carries span bytes: %q", string(clean.Text))
	}
	unmapped := hit
	unmapped.Class = ClassHighEntropy
	tainted, err := rewriter.Rewrite([]byte(text), []DetectionHit{unmapped})
	if err == nil {
		t.Fatal("a hit with no tag type was rewritten anyway")
	}
	if !tainted.Tainted || tainted.Text != nil {
		t.Fatalf("a refused rewrite came back as %+v", tainted)
	}
}

// TestScrubTurnScansAndRewrites drives the composed boundary, and pins
// the one refusal a caller has to know about.
//
// The contract's R-21.240 table is fixed at six rows and maps only the
// classes the pattern registry emits; ClassHighEntropy, the detector's
// shape-only signal, has no row, so a corroborated opaque run is refused
// rather than tagged. That collides with this ticket's own worked example
// ("topic wifi + my password -> WIFI_PASSWORD"), which the detector
// reports as exactly such a high-entropy hit. Refusing is the fail-closed
// reading and is asserted here so the collision is visible; inventing a
// seventh row would have hidden it.
func TestScrubTurnScansAndRewrites(t *testing.T) {
	scrubber, err := NewScrubber(DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("NewScrubber: %v", err)
	}
	text, _ := singleHit()
	result, err := scrubber.ScrubTurn([]byte(text))
	if err != nil {
		t.Fatalf("ScrubTurn: %v", err)
	}
	if string(result.Text) != "OPENAI_API_KEY=<apikey>OPENAI_API_KEY</apikey>\n" {
		t.Fatalf("ScrubTurn = %q", string(result.Text))
	}
	again, err := scrubber.ScrubTurn(result.Text)
	if err != nil || !bytes.Equal(again.Text, result.Text) {
		t.Fatalf("ScrubTurn is not idempotent: %q, %v", string(again.Text), err)
	}
	if _, err := scrubber.ScrubTurn([]byte("wifi password: 7Kq2mZx9PLw4Rt6VbN3sQe8Hj1Cd5Fg0")); err == nil {
		t.Fatal("a corroborated high-entropy span was rewritten despite having no tag type")
	}
	if _, err := NewScrubber(DetectionConfig{}); err == nil {
		t.Fatal("NewScrubber accepted an invalid configuration")
	}
}

// ExampleRewriter_Rewrite shows the contract: the credential becomes a
// vault reference, and the account says what changed without repeating it.
func ExampleRewriter_Rewrite() {
	turn := []byte("deploy with OPENAI_API_KEY=sk-Example000AAAA1111BBBB2222CCCC33 today")
	detector, err := NewDetector(DefaultRegistry(), DefaultDetectionConfig())
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	result, err := NewRewriter().Rewrite(turn, detector.ScanCertain(turn))
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(string(result.Text))
	for _, r := range result.Replacements {
		fmt.Printf("replaced %s at %d+%d with %s\n", r.Class, r.Offset, r.Len, r.Tag)
	}
	fmt.Println("tainted:", result.Tainted)
	// Output:
	// deploy with OPENAI_API_KEY=<apikey>OPENAI_API_KEY</apikey> today
	// replaced api-key at 27+35 with <apikey>OPENAI_API_KEY</apikey>
	// tainted: false
}

// TestRewriteOrderingIsTotal drives the tie-breakers in the overlap rank:
// equal lengths, equal offsets, differing class and differing pattern. The
// hits are fed in two orders and must produce the same bytes, since a rank
// that fell through to arrival order would make a retry non-deterministic.
func TestRewriteOrderingIsTotal(t *testing.T) {
	text := []byte("aaaa OPENAI_API_KEY=one two DATABASE_URL=three four")
	hits := []DetectionHit{
		{Class: ClassAPIKey, Pattern: "b", Offset: 5, Len: 10, SuggestedName: "FIRST_KEY"},
		{Class: ClassJWT, Pattern: "a", Offset: 5, Len: 10, SuggestedName: "SECOND_KEY"},
		{Class: ClassAPIKey, Pattern: "a", Offset: 5, Len: 10, SuggestedName: "THIRD_KEY"},
		{Class: ClassConnString, Pattern: "c", Offset: 28, Len: 10, SuggestedName: "A_URL"},
	}
	reversed := []DetectionHit{hits[3], hits[2], hits[1], hits[0]}
	forward, err := NewRewriter().Rewrite(text, hits)
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	backward, err := NewRewriter().Rewrite(text, reversed)
	if err != nil {
		t.Fatalf("reversed: %v", err)
	}
	if !bytes.Equal(forward.Text, backward.Text) {
		t.Fatalf("hit order changed the output: %q vs %q", string(forward.Text), string(backward.Text))
	}
	if len(forward.Replacements) != 2 {
		t.Fatalf("expected two replacements, got %+v", forward.Replacements)
	}
}
