// Package fleet (top_types.go): the `cascade fleet top` view-model types
//
//	(P1-E18-W4-S40-T1,
//
//	R/S-40.T1) — SessionRow, GovernorPanel, TaskPanel, Summary, and
//	the TopSnapshot that bundles them. These are pure data types with no
//	IO, no clock reads and no rendering: top_fetch.go populates them,
//	top_render.go/top.go consume them, and top_types_test.go exercises
//	their aggregation logic directly (Art.7.3 determinism — no bare
//	time.Now anywhere in this file).
//
// Inputs: none (types + a pure aggregator function).
// Outputs: TopSnapshot, JSON-marshalable for `--once --json` (06 §5.8).
// Constraints: every field is a value type; no pointer aliasing back into
//
//	a fetcher's internal state (mirrors census.Collector.Latest's own
//	copy-on-read discipline).
//
// SPORT: internal.fleet.top/ADDED (P1-E18-W4-S40-T1).
package fleet

import "time"

// TopSessionRow is one fleet session row rendered by the sessions panel,
// mirroring cmd/cascade's own fleetSessionRow column set (session_id,
// harness, state, account, elapsed) plus Lane, which fleetSessionRow does
// not carry today (CONTRACT NOTE: sessions.SessionRecord has no Lane
// field either — Lane is left empty until a session-to-lane attribution
// seam exists; see top_fetch.go's fetch-path doc comment for the
// disclosed gap this mirrors from fleet.go's own "Confidence: n/a" note).
type TopSessionRow struct {
	SessionID string `json:"session_id"`
	Harness   string `json:"harness"`
	State     string `json:"state"`
	Account   string `json:"account"`
	Lane      string `json:"lane,omitempty"`
	Elapsed   string `json:"elapsed"`
}

// GovernorPanel is the resource/admission snapshot the governor panel
// renders. Available is false when no sample could be taken (e.g. the
// platform sampler returned governor.ErrUnsupportedPlatform, or no
// sample has landed yet) — the renderer must show an honest "unavailable"
// state rather than zero values that look like real data.
type GovernorPanel struct {
	Available          bool    `json:"available"`
	CPUFraction        float64 `json:"cpu_fraction,omitempty"`
	MemUsedBytes       uint64  `json:"mem_used_bytes,omitempty"`
	MemTotalBytes      uint64  `json:"mem_total_bytes,omitempty"`
	SwapUsedBytes      uint64  `json:"swap_used_bytes,omitempty"`
	SwapTotalBytes     uint64  `json:"swap_total_bytes,omitempty"`
	QueueDepth         int     `json:"queue_depth,omitempty"`
	CompileLockHolders int     `json:"compile_lock_holders,omitempty"`
	ThrottleTier       string  `json:"throttle_tier,omitempty"`
}

// TaskPanel is the active-ticket panel. Available is false when no
// cascade-pbd ticket context could be resolved (the full_desc's own
// "if cascade-pbd is installed" condition) — this is a disclosed,
// legitimate empty state, not a stub.
type TaskPanel struct {
	Available   bool     `json:"available"`
	TicketID    string   `json:"ticket_id,omitempty"`
	JournalTail []string `json:"journal_tail,omitempty"`
	LastEvent   string   `json:"last_event,omitempty"`
	Unavailable string   `json:"unavailable_reason,omitempty"`
}

// Summary is the one-line fleet-wide rollup: session count, lanes
// busy (distinct non-empty Lane values), and stalled count (State ==
// "stalled", the vocabulary S-25.T1's SessionState enum defines).
type Summary struct {
	SessionCount int `json:"session_count"`
	LanesBusy    int `json:"lanes_busy"`
	StalledCount int `json:"stalled_count"`
}

// TopSnapshot bundles one point-in-time render of all four panels, plus
// GeneratedAt (stamped from an injected clock — never time.Now directly;
// see top_fetch.go's Fetcher).
type TopSnapshot struct {
	Sessions    []TopSessionRow `json:"sessions"`
	Governor    GovernorPanel   `json:"governor"`
	Task        TaskPanel       `json:"task"`
	Summary     Summary         `json:"summary"`
	GeneratedAt time.Time       `json:"generated_at"`
}

// BuildSummary aggregates sessions into a Summary. A nil or
// empty slice yields a zero-value Summary — an explicit empty state,
// never an error.
func BuildSummary(sessions []TopSessionRow) Summary {
	sum := Summary{SessionCount: len(sessions)}
	lanes := make(map[string]struct{}, len(sessions))
	for _, s := range sessions {
		if s.Lane != "" {
			lanes[s.Lane] = struct{}{}
		}
		if s.State == "stalled" {
			sum.StalledCount++
		}
	}
	sum.LanesBusy = len(lanes)
	return sum
}

// NewTopSnapshot builds a TopSnapshot from its four panel inputs,
// deriving Summary from sessions via BuildSummary rather than
// requiring every caller to compute it independently.
func NewTopSnapshot(sessions []TopSessionRow, gov GovernorPanel, task TaskPanel, generatedAt time.Time) TopSnapshot {
	return TopSnapshot{
		Sessions:    sessions,
		Governor:    gov,
		Task:        task,
		Summary:     BuildSummary(sessions),
		GeneratedAt: generatedAt,
	}
}

// UpsertSession returns a copy of sessions with rec inserted (by
// SessionID) or replacing an existing row with the same SessionID —
// used by top_fetch.go's SSE event handler to fold one incoming session-
// changed event into the current snapshot without a full re-fetch.
func UpsertSession(sessions []TopSessionRow, rec TopSessionRow) []TopSessionRow {
	out := make([]TopSessionRow, 0, len(sessions)+1)
	replaced := false
	for _, s := range sessions {
		if s.SessionID == rec.SessionID {
			out = append(out, rec)
			replaced = true
			continue
		}
		out = append(out, s)
	}
	if !replaced {
		out = append(out, rec)
	}
	return out
}
