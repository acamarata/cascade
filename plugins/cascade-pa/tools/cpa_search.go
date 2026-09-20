package tools

// Purpose (this file): cascade_cpa_search — find turns whose content
//   matches a query.
//
// A SCAN, DELIBERATELY, AND SAID SO. At this point in the plan the search
//   is a case-insensitive substring scan over turn content, most recent
//   first, with score 1.0 on every match. The FTS5-backed search replaces
//   it when S-44.T3 lands; the {results} shape defined here is what that
//   swap preserves, which is why the score field exists now and is
//   constant rather than being added later and changing the contract.
//   No semantic or embedding-distance search is in scope for this epic.
//
// SPORT: plugins/cascade-pa:mcp-tools:cascade_cpa_search (ADD) — P1-E20-W5-S43-T4.

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/acamarata/cascade/pkg/cascade"
)

// DefaultSearchLimit is the result count when a caller names none.
const DefaultSearchLimit = 10

// excerptRadius is how much context surrounds a match in the excerpt.
const excerptRadius = 60

// SearchInput is cascade_cpa_search's parameter schema.
type SearchInput struct {
	// Query is the substring to look for (required).
	Query string `json:"query"`
	// ThreadID narrows the search to one thread.
	ThreadID string `json:"thread_id,omitempty"`
	// Limit bounds the results (default 10, clamped to 100).
	Limit int `json:"limit,omitempty"`
}

// SearchHit is one match.
type SearchHit struct {
	ThreadID string  `json:"thread_id"`
	TurnID   string  `json:"turn_id"`
	Excerpt  string  `json:"excerpt"`
	Score    float64 `json:"score"`
}

// SearchOutput is cascade_cpa_search's result.
type SearchOutput struct {
	Results []SearchHit `json:"results"`
}

// search scans for the query.
func (d *Dispatcher) search(ctx context.Context, input []byte) ([]byte, error) {
	in, err := decodeToolInput[SearchInput](ToolSearch, input)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Query) == "" {
		// An empty query is refused rather than answered with
		// everything: every turn contains the empty string, so a
		// caller's blank field would look like a working search that
		// found the whole store.
		return nil, cascade.Newf(cascade.KindInvalidInput,
			"%s: query is empty; every turn matches the empty string, which is not a search", ToolSearch)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	if limit > MaxHistoryLimit {
		limit = MaxHistoryLimit
	}
	found, err := d.hits(ctx, in, limit)
	if err != nil {
		return nil, err
	}
	return json.Marshal(SearchOutput{Results: found})
}

// hits answers from the full-text index, falling back to the scan.
//
// THE FALLBACK IS NOT BELT-AND-BRACES. S-44.T3's FTS5 index is created by
// ensureFTS5, a raw DDL statement the typed migration builder cannot
// express, and only on the SQLite dialect — so a store on another dialect,
// or one whose index could not be built, has nothing to query. Reporting
// "no matches" there would tell an agent its operator has never written the
// words they are looking at. The scan is slower and unranked, and it is a
// true answer.
func (d *Dispatcher) hits(ctx context.Context, in SearchInput, limit int) ([]SearchHit, error) {
	found, err := d.svc.Search(ctx, SearchRequest{Query: in.Query, ThreadID: in.ThreadID, Limit: limit})
	if err != nil {
		// Identity, not errors.Is: see ErrSearchUnavailable's own comment.
		if err == ErrSearchUnavailable {
			// Only NOW resolve which threads to walk. Listing every thread
			// up front cost a round trip on the healthy path, where the
			// index answers without needing them at all.
			threads, scopeErr := d.searchScope(ctx, in.ThreadID)
			if scopeErr != nil {
				return nil, scopeErr
			}
			return scanThreads(ctx, d.svc, threads, in.Query, limit), nil
		}
		// Every other failure is REPORTED. Returning an empty result set
		// would tell an agent its operator has written nothing matching,
		// when what actually happened is that the search did not run.
		return nil, err
	}
	out := make([]SearchHit, 0, len(found))
	for _, f := range found {
		idx := strings.Index(strings.ToLower(f.Content), strings.ToLower(in.Query))
		if idx < 0 {
			// The index matched on a stem or a token this substring search
			// cannot locate. Show the head of the turn rather than nothing.
			idx = 0
		}
		out = append(out, SearchHit{
			ThreadID: f.ThreadID, TurnID: f.TurnID,
			Excerpt: excerptAround(f.Content, idx, len(in.Query)),
			Score:   f.Score,
		})
	}
	return out, nil
}

// searchScope resolves which threads to scan.
func (d *Dispatcher) searchScope(ctx context.Context, threadID string) ([]string, error) {
	if threadID != "" {
		return []string{threadID}, nil
	}
	summaries, err := d.svc.Threads(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(summaries))
	for _, s := range summaries {
		ids = append(ids, s.ID)
	}
	return ids, nil
}

// scanThreads walks each thread newest-turn-first, collecting matches
// until limit is reached.
//
// A thread that cannot be read is SKIPPED rather than failing the whole
// search: one unreadable thread should not make the other nine
// unsearchable, and the caller sees fewer results rather than none.
func scanThreads(ctx context.Context, svc Conversations, threadIDs []string, query string, limit int) []SearchHit {
	needle := strings.ToLower(query)
	hits := make([]SearchHit, 0, limit)
	for _, id := range threadIDs {
		detail, err := svc.Thread(ctx, id)
		if err != nil {
			continue
		}
		for i := len(detail.Turns) - 1; i >= 0; i-- {
			turn := detail.Turns[i]
			idx := strings.Index(strings.ToLower(turn.Content), needle)
			if idx < 0 {
				continue
			}
			hits = append(hits, SearchHit{
				ThreadID: id, TurnID: turn.TurnID,
				Excerpt: excerptAround(turn.Content, idx, len(query)),
				// Constant by construction: a substring scan has no
				// relevance to report, and a fabricated gradient would
				// be a ranking nobody computed.
				Score: 1.0,
			})
			if len(hits) >= limit {
				return hits
			}
		}
	}
	return hits
}

// excerptAround returns the match plus surrounding context, with ellipses
// where the content was cut.
func excerptAround(content string, idx, matchLen int) string {
	start := idx - excerptRadius
	if start < 0 {
		start = 0
	}
	end := idx + matchLen + excerptRadius
	if end > len(content) {
		end = len(content)
	}
	// The radius is a BYTE count, so either edge can land inside a
	// multi-byte rune. A half rune reaches the operator as U+FFFD — a
	// replacement character standing where a letter they wrote used to
	// be, in a transcript of their own words. Widen each edge outward to
	// the nearest rune boundary; widening can only add context, never
	// drop a character that was already inside the window.
	//
	// excerptRadius is 60, which is divisible by 1, 2, 3 and 4, so a run
	// of uniformly-sized runes never trips this. It takes a MIXED-width
	// run — one ASCII character among CJK, say — to knock the offset off
	// alignment, which is why the first version of this went unnoticed.
	for start > 0 && !utf8.RuneStart(content[start]) {
		start--
	}
	for end < len(content) && !utf8.RuneStart(content[end]) {
		end++
	}
	out := content[start:end]
	if start > 0 {
		out = "…" + out
	}
	if end < len(content) {
		out += "…"
	}
	return out
}
