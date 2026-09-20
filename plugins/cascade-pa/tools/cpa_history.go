package tools

// Purpose (this file): cascade_cpa_history — a thread's turns, or the
//   thread listing when no thread is named.
// SPORT: plugins/cascade-pa:mcp-tools:cascade_cpa_history (ADD) — P1-E20-W5-S43-T4.

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The history page bounds.
const (
	// DefaultHistoryLimit is the page size when a caller names none.
	DefaultHistoryLimit = 20
	// MaxHistoryLimit is the ceiling. A larger request is CLAMPED, not
	// refused: an agent asking for a thousand turns wants as much as it
	// can have, and an error would make it ask again for a number it has
	// to guess.
	MaxHistoryLimit = 100
)

// HistoryInput is cascade_cpa_history's parameter schema.
type HistoryInput struct {
	// ThreadID selects one thread's turns. Omitted, the result is the
	// thread listing instead.
	ThreadID string `json:"thread_id,omitempty"`
	// Limit bounds the page (default 20, clamped to 100).
	Limit int `json:"limit,omitempty"`
	// BeforeTurnID is the cursor: the page ends immediately before it.
	BeforeTurnID string `json:"before_turn_id,omitempty"`
}

// HistoryOutput is cascade_cpa_history's result. Items are turns when a
// thread was named and thread summaries when one was not; the shape is
// one field either way so a caller parses one document.
type HistoryOutput struct {
	Items      []json.RawMessage `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

// ResolveLimit applies the default and the ceiling.
//
// A zero or negative limit is the default rather than an error: zero is
// what an omitted field decodes to, and a caller that asked for nothing
// wants the default page, not a refusal.
func ResolveLimit(v int) int {
	switch {
	case v <= 0:
		return DefaultHistoryLimit
	case v > MaxHistoryLimit:
		return MaxHistoryLimit
	default:
		return v
	}
}

// history answers with turns or with the thread listing.
func (d *Dispatcher) history(ctx context.Context, input []byte) ([]byte, error) {
	in, err := decodeToolInput[HistoryInput](ToolHistory, input)
	if err != nil {
		return nil, err
	}
	if in.ThreadID == "" {
		return d.threadListing(ctx)
	}
	return d.threadTurns(ctx, in)
}

// threadListing answers the no-thread-named case.
func (d *Dispatcher) threadListing(ctx context.Context) ([]byte, error) {
	threads, err := d.svc.Threads(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]json.RawMessage, 0, len(threads))
	for _, t := range threads {
		row, merr := json.Marshal(map[string]string{
			"id": t.ID, "title": t.Title, "updated_at": t.UpdatedAt,
		})
		if merr != nil {
			return nil, cascade.Wrapf(cascade.KindInternal, merr, "%s: encoding a thread row", ToolHistory)
		}
		items = append(items, row)
	}
	return json.Marshal(HistoryOutput{Items: items})
}

// threadTurns answers one thread's page.
func (d *Dispatcher) threadTurns(ctx context.Context, in HistoryInput) ([]byte, error) {
	detail, err := d.svc.Thread(ctx, in.ThreadID)
	if err != nil {
		return nil, err
	}
	page, next := pageTurns(detail.Turns, in.BeforeTurnID, ResolveLimit(in.Limit))
	items := make([]json.RawMessage, 0, len(page))
	for _, turn := range page {
		row, merr := json.Marshal(map[string]any{
			"turn_id": turn.TurnID, "role": turn.Role, "content": turn.Content,
			"created_at": turn.CreatedAt, "seq": turn.Seq,
		})
		if merr != nil {
			return nil, cascade.Wrapf(cascade.KindInternal, merr, "%s: encoding a turn", ToolHistory)
		}
		items = append(items, row)
	}
	return json.Marshal(HistoryOutput{Items: items, NextCursor: next})
}

// pageTurns returns the newest `limit` turns ending before cursor, oldest
// first, plus the cursor for the page before it.
//
// NEWEST FIRST is what a cursor of "" means here: an agent opening a
// thread wants its recent end, and pages backwards from there. Which turns
// are SELECTED is therefore driven from the end of the slice; the order
// WITHIN the page is the store's own, oldest first, because that is the
// order a conversation reads in. Nothing is re-ordered — the slice
// preserves what Thread returned.
//
// A cursor naming a turn this thread does not contain returns the newest
// page rather than an error. The turn may have been retained away since
// the agent read it, and refusing would strand a caller holding a cursor
// it has no way to repair.
func pageTurns(turns []TurnRecord, cursor string, limit int) ([]TurnRecord, string) {
	end := len(turns)
	if cursor != "" {
		for i, t := range turns {
			if t.TurnID == cursor {
				end = i
				break
			}
		}
	}
	start := end - limit
	if start < 0 {
		start = 0
	}
	page := turns[start:end]
	next := ""
	if start > 0 {
		next = turns[start].TurnID
	}
	return page, next
}
