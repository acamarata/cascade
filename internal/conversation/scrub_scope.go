package conversation

// Purpose: the scrub pipeline's TURN SCOPE -- joining a turn's segments
//   into the one buffer the detector scans, mapping each hit back to the
//   segment that holds it, and refusing a hit that straddles a boundary.
//   Split from scrub.go under Art.10.3's 300-line file cap.
// Inputs: the turn's segments in order, and the hits a turn-wide
//   ScanCertain returned over their concatenation.
// Outputs: per-segment hits in SEGMENT-LOCAL coordinates, or
//   ErrScrubSpanStraddlesSegments.
// Constraints: the join carries NO separator bytes. A separator would be
//   invented content: it can split a credential that the reader sees as
//   one run of characters (hiding it from the detector) and it can just as
//   easily manufacture a pattern that is not in the turn at all. So the
//   scanned buffer is exactly the turn's own bytes, and the only thing
//   that can go wrong -- a hit crossing a boundary -- is refused rather
//   than rewritten, because rewriting across a boundary would move bytes
//   from one stored segment into another.
// SPORT: internal.conversation.scrub/ADDED (P1-E20-W5-S44-T1).

import (
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrScrubSpanStraddlesSegments is the detect phase's refusal: a
// certain-confidence span begins in one segment of the turn and ends in
// another. Fail closed rather than rewrite across segments -- the two
// halves are stored as two rows, and a tag can only stand in for bytes
// inside the row it lands in. The message names the phase and says
// nothing about the content, per errors.go's static-literal rule.
var ErrScrubSpanStraddlesSegments = cascade.New(cascade.KindPolicyDenied,
	"conversation: the scrub detect phase found a secret spanning two segments of one turn; turn refused")

// segSpan is one segment's half-open [start,end) range inside the joined
// turn buffer.
type segSpan struct {
	start int
	end   int
}

// joinSegments concatenates the turn's segments in order and returns the
// buffer plus each segment's range inside it.
func joinSegments(segs []ScrubSegment) ([]byte, []segSpan) {
	total := 0
	for _, seg := range segs {
		total += len(seg.Content)
	}
	joined := make([]byte, 0, total)
	bounds := make([]segSpan, len(segs))
	for i, seg := range segs {
		bounds[i] = segSpan{start: len(joined), end: len(joined) + len(seg.Content)}
		joined = append(joined, seg.Content...)
	}
	return joined, bounds
}

// contentsOf returns each segment's content unchanged -- the no-hit
// passthrough result, which must be one slice per input segment like
// every other return.
func contentsOf(segs []ScrubSegment) [][]byte {
	out := make([][]byte, len(segs))
	for i, seg := range segs {
		out[i] = seg.Content
	}
	return out
}

// turnRef is the ref a turn-level divergence event is addressed by: the
// first segment's. Every segment of one turn shares that turn's id
// (domain.go's NewSegmentID is an address over turnID/seq/kind), so the
// first one identifies the turn without naming a span.
func turnRef(segs []ScrubSegment) string {
	if len(segs) == 0 {
		return ""
	}
	return segs[0].Ref
}

// splitHitsBySegment maps every turn-scoped hit onto the segment that
// wholly contains it, rebasing Offset to that segment's own bytes. A hit
// no single segment contains straddles a boundary and refuses the turn.
func splitHitsBySegment(hits []secrets.DetectionHit, bounds []segSpan) ([][]secrets.DetectionHit, error) {
	out := make([][]secrets.DetectionHit, len(bounds))
	for _, hit := range hits {
		idx := containingSegment(bounds, hit)
		if idx < 0 {
			return nil, ErrScrubSpanStraddlesSegments
		}
		local := hit
		local.Offset = hit.Offset - bounds[idx].start
		out[idx] = append(out[idx], local)
	}
	return out, nil
}

// containingSegment returns the index of the segment wholly containing
// hit, or -1. An empty segment contains nothing: a hit always spans at
// least one byte (the detector's own contract), so start==end can never
// hold one, and skipping those keeps a zero-length segment from claiming
// a hit that belongs to its neighbour.
func containingSegment(bounds []segSpan, hit secrets.DetectionHit) int {
	end := hit.Offset + hit.Len
	for i, span := range bounds {
		if span.end <= span.start {
			continue
		}
		if hit.Offset >= span.start && end <= span.end {
			return i
		}
	}
	return -1
}

// spanOf reads hit's bytes out of one segment's content. Bounds are
// guaranteed by ScanCertain's own contract plus splitHitsBySegment's
// containment check above, never re-validated by construction here.
func spanOf(content []byte, hit secrets.DetectionHit) []byte {
	return content[hit.Offset : hit.Offset+hit.Len]
}
