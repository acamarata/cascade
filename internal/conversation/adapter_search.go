package conversation

// Purpose (this file): chat.search's handler, split from adapter.go for
//   Art.10.3's 300-line cap -- the same split adapter_wire.go's own header
//   already documents for this file's wire shapes, made necessary again by
//   P1-E21-W5-S46-T4 D2's topic-engine hook growing handleAppendTurn's
//   neighbourhood in adapter.go.
// SPORT: internal.conversation.adapter (CHANGED -- search handler split,
//   P1-E21-W5-S46-T4).

import (
	"context"
	"encoding/json"
)

// handleSearch is chat.search: the FTS5 index, over the wire.
//
// WHY THIS EXISTS. S-44.T3 built SearchTurns, PruneTurns and thread
// archival into this package and stopped at its boundary: nothing outside
// internal/conversation called any of them, so the FTS5 index this ticket
// created could not be reached by a running program, and
// cascade_cpa_search went on running the plain store scan the index was
// written to replace. A capability with no caller is not a feature
// (R-14.283).
//
// The rank is REPORTED AS A SCORE, negated. SQLite's bm25() is
// lower-is-better and returns negative values; a field named "score" is
// read higher-is-better by everything that consumes one. Negating is a
// monotonic relabelling of SQLite's own number — it invents no ranking.
func (a *Adapter) handleSearch(ctx context.Context, raw json.RawMessage) (any, error) {
	p, err := decodeSearchParams(raw)
	if err != nil {
		return nil, err
	}
	matches, err := a.store.SearchTurns(ctx, p.Query, SearchFilter{ThreadID: p.ThreadID, Limit: p.Limit})
	if err != nil {
		return nil, err
	}
	out := make([]searchResultWire, 0, len(matches))
	for _, m := range matches {
		segs, segErr := a.store.ListSegments(ctx, m.Turn.ID)
		if segErr != nil {
			return nil, segErr
		}
		out = append(out, searchResultWire{
			ThreadID: m.Turn.ThreadID,
			TurnID:   m.Turn.ID,
			Content:  joinSegmentContent(segs),
			Score:    -m.Rank,
		})
	}
	return searchResultSet{Results: out}, nil
}
