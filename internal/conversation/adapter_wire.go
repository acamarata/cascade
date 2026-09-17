package conversation

// Purpose (this file): chat.append_turn's WIRE shapes and their decoders —
//   what a caller sends, what it gets back, and what turns one into
//   stored records. Split from adapter.go for Art.10.3's 300-line cap.
// SPORT: internal.conversation.adapter (CHANGED — wire split,
//   P1-E20-W5-S43-T4).

import (
	"bytes"
	"encoding/json"
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
