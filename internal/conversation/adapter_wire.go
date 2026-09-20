package conversation

// Purpose (this file): chat.append_turn's WIRE shapes and their decoders —
//   what a caller sends, what it gets back, and what turns one into
//   stored records. Split from adapter.go for Art.10.3's 300-line cap.
// SPORT: internal.conversation.adapter (CHANGED — wire split,
//   P1-E20-W5-S43-T4).

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// appendTurnParams is chat.append_turn's wire request shape: one turn's
// role plus its ordered content segments. Unknown fields are rejected
// (DisallowUnknownFields) so a caller typo surfaces immediately rather
// than being silently dropped.
type appendTurnParams struct {
	ThreadID string              `json:"thread_id"`
	Role     string              `json:"role"`
	Segments []appendSegmentWire `json:"segments"`
}

type appendSegmentWire struct {
	Kind    string `json:"kind"`
	Content string `json:"content"`
}

// appendTurnResult is chat.append_turn's wire response shape.
type appendTurnResult struct {
	ThreadID string `json:"thread_id"`
	TurnID   string `json:"turn_id"`
	Seq      int64  `json:"seq"`
}

func decodeAppendTurnParams(raw json.RawMessage) (appendTurnParams, error) {
	var p appendTurnParams
	if len(raw) == 0 {
		return p, ErrMalformedTurnPayload
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return appendTurnParams{}, ErrMalformedTurnPayload
	}
	// An empty ThreadID is NOT malformed: it means "start a new thread",
	// and handleAppendTurn mints the id. A missing role still is —
	// nothing can infer who spoke (R-14.285).
	if p.Role == "" {
		return appendTurnParams{}, ErrMalformedTurnPayload
	}
	return p, nil
}

// decodeSegments turns one append's wire segments into stored Segments.
//
// Factored out of handleAppendTurn for Art.10.3's 50-line cap. An
// unrecognised kind is the whole turn's refusal, not a dropped segment: a
// turn stored with half its content is worse than one refused, because
// the caller is told it was recorded.
func decodeSegments(wire []appendSegmentWire, turnID string, now int64) ([]Segment, error) {
	out := make([]Segment, 0, len(wire))
	for i, sw := range wire {
		kind, err := DecodeSegmentKind(sw.Kind)
		if err != nil {
			return nil, ErrMalformedTurnPayload
		}
		out = append(out, Segment{ID: NewSegmentID(turnID, int64(i), kind), TurnID: turnID, Seq: int64(i),
			Kind: kind, Content: sw.Content, CreatedAt: now})
	}
	return out, nil
}

// searchParams is chat.search's wire request shape.
//
// The same three fields cascade_cpa_search takes, because that tool is why
// this method exists: S-44.T3's FTS5 backend replaces the plain store scan
// S-43.T4 shipped, and a wire shape that did not match would have made the
// swap a rewrite of the tool rather than a change of backend.
type searchParams struct {
	Query    string `json:"query"`
	ThreadID string `json:"thread_id"`
	Limit    int    `json:"limit"`
}

// searchResultWire is one hit.
//
// CONTENT, NOT AN EXCERPT. Cutting an excerpt out of a turn is a rendering
// decision — how much context, where to elide, and (as S-43.T4 found the
// hard way) how to avoid cutting a multi-byte rune in half. That belongs to
// the surface doing the rendering, not to the daemon. This carries the
// matching turn's content and lets the caller cut it.
type searchResultWire struct {
	ThreadID string  `json:"thread_id"`
	TurnID   string  `json:"turn_id"`
	Content  string  `json:"content"`
	Score    float64 `json:"score"`
}

// searchResultSet is chat.search's wire response shape.
type searchResultSet struct {
	Results []searchResultWire `json:"results"`
}

// decodeSearchParams decodes chat.search's params, refusing unknown fields
// and an empty query.
//
// An empty query is refused rather than answered: FTS5 would reject it
// anyway, and the refusal an operator reads should say what was wrong with
// the request rather than surfacing a syntax error from the index.
func decodeSearchParams(raw json.RawMessage) (searchParams, error) {
	var p searchParams
	if len(raw) == 0 {
		return p, ErrMalformedTurnPayload
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return searchParams{}, ErrMalformedTurnPayload
	}
	if strings.TrimSpace(p.Query) == "" {
		return searchParams{}, cascade.New(cascade.KindInvalidInput,
			"conversation: chat.search requires a non-empty query")
	}
	return p, nil
}

// joinSegmentContent flattens a turn's segments into the one string the
// search surface reports. Every kind contributes: a code or tool_result
// segment dropped here would make a turn findable by the prose around it
// and then show none of what actually matched.
func joinSegmentContent(segs []Segment) string {
	parts := make([]string, 0, len(segs))
	for _, s := range segs {
		if s.Content != "" {
			parts = append(parts, s.Content)
		}
	}
	return strings.Join(parts, "\n\n")
}
