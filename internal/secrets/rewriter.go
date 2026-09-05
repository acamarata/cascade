// Purpose: the turn-rewrite engine. Rewriter.Rewrite takes a turn and
//
//	the detector's hits and returns the turn with every credential span
//	replaced by a typed tag, plus the record of what it replaced.
//	Scrubber is the composed entry point a boundary calls: scan, then
//	rewrite, with one configuration.
//
// Inputs: the original turn bytes and []DetectionHit. Nothing else: no
//
//	file, no clock, no network, no randomness.
//
// Outputs: a RewriteResult carrying the rewritten bytes, one Replacement
//
//	per tagged span, and a taint bit. On any error the result carries no
//	text at all and stays tainted, so there is no path that returns
//	partially-rewritten bytes marked clean.
//
// Constraints: this code alters what a person wrote, which makes it both
//
//	a privacy control and a correctness hazard. Three rules follow from
//	that. It is ACCOUNTED: every alteration appears in Replacements with
//	its offset, class and tag, so a user can be told exactly what
//	changed. It NEVER WIDENS: a Replacement holds no span bytes in any
//	encoding, and neither does an error message, so the output and every
//	diagnostic are strictly less disclosing than the input. It is
//	IDEMPOTENT and deterministic: spans already carrying a valid tag are
//	left alone, and identical input yields identical output byte for
//	byte.
//
// SPORT: TURN_REWRITER: ADD (internal/secrets.Rewriter, Scrubber).

package secrets

import (
	"bytes"
	"sort"
	"unicode/utf8"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Replacement records one alteration the rewriter made. Offset and Len
// address the ORIGINAL text, so a caller can show a user the before and
// the after. There is deliberately no field that could hold the replaced
// bytes: an accounted rewrite has to be explainable without re-disclosing
// what it protected, and leaving the field out makes that a compile-time
// property rather than a review note.
type Replacement struct {
	// Offset is the byte offset of the replaced span in the original.
	Offset int `json:"offset"`
	// Len is the replaced span's length in bytes in the original.
	Len int `json:"len"`
	// Class is the detection class that fired on the span.
	Class Class `json:"class"`
	// Pattern names the detector pattern; diagnostic only.
	Pattern string `json:"pattern"`
	// Confidence is the score the hit carried.
	Confidence Confidence `json:"confidence"`
	// Tag is what the span was replaced with.
	Tag Tag `json:"tag"`
}

// RewriteResult is a rewritten turn together with its account.
type RewriteResult struct {
	// Text is the rewritten turn. Nil only when Rewrite returned an
	// error; an empty turn rewrites to an empty, non-nil slice.
	Text []byte `json:"text"`
	// Replacements lists every alteration, ordered by Offset.
	Replacements []Replacement `json:"replacements"`
	// Tainted reports whether the text may still carry credential
	// material. It is false only when the rewrite completed and every
	// detected span ended up behind a typed tag; every error path
	// returns Tainted true with no text, so untainted text that still
	// holds a detected span is not a value this package can produce.
	Tainted bool `json:"tainted"`
}

// Rewriter substitutes detector hits for typed tags. It holds no state
// and is safe for concurrent use; the zero value is ready.
type Rewriter struct{}

// NewRewriter returns a Rewriter.
func NewRewriter() *Rewriter { return &Rewriter{} }

// Rewrite replaces every hit span in text with the typed tag for its
// class, naming each tag from the hit's SuggestedName.
//
// Overlaps resolve largest-span-first: a hit enclosed by another is
// dropped, since the enclosing tag already covers those bytes and two
// tags over one span would double-count a single secret. Spans that
// already carry a valid tag are left untouched, which is what makes a
// second pass a no-op.
//
// It fails closed. A hit that addresses bytes outside text, a hit that
// bisects a multi-byte rune, a hit whose suggested name is not a legal
// tag NAME, a hit that partially overlaps an existing tag, text that is
// not valid UTF-8, and a class with no tag type all return an error with
// no text rather than a best-effort rewrite: emitting a turn that is
// almost scrubbed is the failure this engine exists to prevent.
func (r *Rewriter) Rewrite(text []byte, hits []DetectionHit) (RewriteResult, error) {
	if !utf8.Valid(text) {
		return failedRewrite(cascade.New(cascade.KindInvalidInput,
			"secrets: a turn must be valid UTF-8 before it can be rewritten"))
	}
	tagged := scanTags(string(text))
	pending, err := admissibleHits(text, hits, tagged)
	if err != nil {
		return failedRewrite(err)
	}
	replacements, err := planReplacements(pending)
	if err != nil {
		return failedRewrite(err)
	}
	return RewriteResult{Text: applyReplacements(text, replacements), Replacements: replacements}, nil
}

// failedRewrite is the single error return. It exists so that no error
// path can accidentally hand back text or a cleared taint bit.
func failedRewrite(err error) (RewriteResult, error) {
	return RewriteResult{Tainted: true}, err
}

// admissibleHits validates each hit against the text and drops the ones
// already covered by a tag. A hit that only partially overlaps a tag is
// an error, not a drop: the text and the hits disagree about where the
// credential is, and neither answer can be trusted.
func admissibleHits(text []byte, hits []DetectionHit, tagged []tagSpan) ([]DetectionHit, error) {
	var out []DetectionHit
	for _, hit := range hits {
		if err := checkSpan(text, hit); err != nil {
			return nil, err
		}
		covered, partial := coverage(tagged, hit.Offset, hit.Offset+hit.Len)
		if partial {
			return nil, cascade.Newf(cascade.KindInvalidInput,
				"secrets: a %s hit at byte %d straddles an existing tag; refusing to rewrite",
				string(hit.Class), hit.Offset)
		}
		if covered {
			continue
		}
		out = append(out, hit)
	}
	return out, nil
}

// checkSpan asserts that a hit addresses real bytes on rune boundaries.
// A bisected rune is refused rather than truncated: cutting a multi-byte
// character in half would corrupt the user's text around the tag, which
// they would see immediately and rightly not forgive.
func checkSpan(text []byte, hit DetectionHit) error {
	if hit.Offset < 0 || hit.Len <= 0 || hit.Offset+hit.Len > len(text) {
		return cascade.Newf(cascade.KindInvalidInput,
			"secrets: a %s hit at byte %d spanning %d bytes is outside the %d-byte turn",
			string(hit.Class), hit.Offset, hit.Len, len(text))
	}
	end := hit.Offset + hit.Len
	if !utf8.RuneStart(text[hit.Offset]) || (end < len(text) && !utf8.RuneStart(text[end])) {
		return cascade.Newf(cascade.KindInvalidInput,
			"secrets: a %s hit at byte %d does not start and end on character boundaries",
			string(hit.Class), hit.Offset)
	}
	return nil
}

// coverage reports whether [start,end) lies inside an existing tag, and
// whether it merely overlaps one.
func coverage(tagged []tagSpan, start, end int) (covered, partial bool) {
	for _, span := range tagged {
		if start >= span.start && end <= span.end {
			return true, false
		}
		if start < span.end && span.start < end {
			return false, true
		}
	}
	return false, false
}

// planReplacements resolves overlaps and turns each surviving hit into a
// Replacement. The ranking is total (length, then offset, then class,
// then pattern) so the winner never depends on the order hits arrived in.
func planReplacements(hits []DetectionHit) ([]Replacement, error) {
	ranked := append([]DetectionHit(nil), hits...)
	sort.SliceStable(ranked, func(i, j int) bool { return enclosingFirst(ranked[i], ranked[j]) })
	var kept []DetectionHit
	for _, candidate := range ranked {
		if !overlapsAny(kept, candidate) {
			kept = append(kept, candidate)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Offset < kept[j].Offset })
	out := make([]Replacement, 0, len(kept))
	for _, hit := range kept {
		tagType, err := TagFor(hit.Class)
		if err != nil {
			return nil, err
		}
		tag := Tag{Type: tagType, Name: hit.SuggestedName}
		if err := tag.Validate(); err != nil {
			return nil, err
		}
		out = append(out, Replacement{
			Offset: hit.Offset, Len: hit.Len, Class: hit.Class,
			Pattern: hit.Pattern, Confidence: hit.Confidence, Tag: tag,
		})
	}
	return out, nil
}

// enclosingFirst ranks the larger span first, so an enclosing hit is
// always chosen before the sub-spans it swallows.
func enclosingFirst(a, b DetectionHit) bool {
	switch {
	case a.Len != b.Len:
		return a.Len > b.Len
	case a.Offset != b.Offset:
		return a.Offset < b.Offset
	case a.Class != b.Class:
		return a.Class < b.Class
	default:
		return a.Pattern < b.Pattern
	}
}

// applyReplacements splices the tags in. Replacements are ordered by
// offset and do not overlap, so one pass suffices.
func applyReplacements(text []byte, replacements []Replacement) []byte {
	var out bytes.Buffer
	out.Grow(len(text))
	cursor := 0
	for _, r := range replacements {
		out.Write(text[cursor:r.Offset])
		out.WriteString(r.Tag.String())
		cursor = r.Offset + r.Len
	}
	out.Write(text[cursor:])
	if out.Len() == 0 {
		return []byte{}
	}
	return out.Bytes()
}

// Scrubber is the composed rewrite boundary: one detector, one rewriter,
// one configuration. A caller that has a turn and wants it safe to store
// or forward uses this rather than wiring the two halves itself, so the
// scan that decides what is a secret and the rewrite that acts on it can
// never be configured differently from each other.
type Scrubber struct {
	detector *Detector
	rewriter *Rewriter
}

// NewScrubber builds a scrubber over the default pattern registry,
// configured by cfg. An invalid cfg is refused, exactly as the detector
// refuses it: a scrubber running under a threshold nobody chose is worse
// than no scrubber at all, because it looks like one.
func NewScrubber(cfg DetectionConfig) (*Scrubber, error) {
	detector, err := NewDetector(DefaultRegistry(), cfg)
	if err != nil {
		return nil, err
	}
	return &Scrubber{detector: detector, rewriter: NewRewriter()}, nil
}

// ScrubTurn scans text and rewrites every hit that reaches the configured
// confidence threshold into a typed tag. It is idempotent: scrubbing an
// already-scrubbed turn returns the same bytes.
//
// It fails closed, and one refusal is worth naming. A corroborated
// high-entropy span has no tag type (see TagFor), so a turn containing
// one is refused rather than emitted with the span still in it. The
// alternative would be to invent a tag type for a span whose kind the
// detector could not name, and to rehydrate it later as something it may
// not be.
func (s *Scrubber) ScrubTurn(text []byte) (RewriteResult, error) {
	return s.rewriter.Rewrite(text, s.detector.ScanCertain(text))
}
