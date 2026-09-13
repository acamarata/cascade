// Purpose (this file): the WidgetSnapshot row shapes (P1-E38-W8-S74-T1,
// R-16.37 §Surfaces NORMATIVE) and Compose, the pure reducer from a real
// FleetSnapshot (S-63.T1, this package) plus the two in-process counts the
// daemon composition root supplies (attention_count, active_jobs_count)
// into the exact vocabulary the macOS widget (Epic AL, apps/widget-macos,
// not built by this ticket) renders.
//
// Inputs: a FleetSnapshot, an attention-queue count, an optional jobs-
// domain active count, and the caller's clock reading (never a bare
// time.Now — Art.7.3).
// Outputs: a WidgetSnapshot. Compose never touches I/O and never invents a
// value: a bucket with no real percentage source (WindowUtilizationUnknown)
// serialises its WidgetWindow as JSON null, and a nil activeJobsCount
// input stays nil on the output (the "daemon has no jobs domain" case).
//
// Constraints: pure function, no bare time.Now (Art.7.3) — see this file's
// own Redact (widget_redact.go) for the R-21.162/R-21.200 label and PII
// rules, split out under the repo's 300-line file cap, the same remedy
// internal/fleet/capacity/compositor_build.go's own header already
// documents for this package.
//
// CONTRACT DEVIATION (naming, recorded — R-16.79). The ticket's task 1
// names a `SessionScopeRef` param type for the RPC layer and describes
// WidgetSnapshot.nodes as built from "FleetSnapshot.NodeSlot" without
// naming FleetSnapshot.Providers/Nodes as the maps they really are
// (snapshot.go: `map[string]ProviderSlot`, `map[string]NodeSlot`, not a
// list) — Compose iterates those two maps directly, sorting keys for
// deterministic row order (never Go's randomized map iteration order).
//
// SPORT: fleet.capacity.widget (ADD, P1-E38-W8-S74-T1).

package capacity

import (
	"sort"
	"time"
)

// WidgetWindow is one rolling accounting window's utilization reading.
// Both fields are pointers so a stale/absent source serialises as JSON
// null — the ticket's never-fabricate rule — rather than a fabricated
// zero that would read as "confirmed empty".
type WidgetWindow struct {
	UtilizationPct *float64       `json:"utilization_pct"`
	ResetsIn       *time.Duration `json:"resets_in"`
}

// WidgetRow is one profile/pool row (R-16.37's DECIDED field set). Ref is
// an opaque stable id — never a hostname, remote URL or absolute path
// (R-21.162); Label is a neutral display label (R-21.200).
type WidgetRow struct {
	Ref            string        `json:"ref"`
	Label          string        `json:"label"`
	Kind           string        `json:"kind"`
	FiveHour       *WidgetWindow `json:"five_hour"`
	SevenDay       *WidgetWindow `json:"seven_day"`
	State          string        `json:"state"`
	ReauthRequired bool          `json:"reauth_required"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

// NodePresenceSummary is one node's presence row, mapped directly from
// FleetSnapshot.Nodes (S-63.T1) — no new source needed.
type NodePresenceSummary struct {
	ID        string        `json:"id"`
	Presence  PresenceState `json:"presence"`
	Reachable bool          `json:"reachable"`
	TrustTier string        `json:"trust_tier"`
}

// ProjectRow is one project/scope row. No production source populates
// this today — see this file's DISCLOSED GAP note on Compose below; a
// nil/empty Projects slice is the honest, never-fabricated result until
// one exists.
type ProjectRow struct {
	Ref         string    `json:"ref"`
	Label       string    `json:"label"`
	TaskSummary *string   `json:"task_summary"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// WidgetSnapshot is the full status.widget payload (R-16.37).
// ActiveJobsCount is nil (client renders "-") only when the daemon has no
// jobs domain (R-16.50) — never a fabricated zero.
type WidgetSnapshot struct {
	Rows            []WidgetRow           `json:"rows"`
	AttentionCount  int                   `json:"attention_count"`
	ActiveJobsCount *int                  `json:"active_jobs_count"`
	Nodes           []NodePresenceSummary `json:"nodes"`
	Projects        []ProjectRow          `json:"projects"`
	GeneratedAt     time.Time             `json:"generated_at"`
	Seq             uint64                `json:"seq"`
}

// windowUnknown reports whether w carries no real percentage source
// (snapshot.go's WindowUtilizationUnknown sentinel) — the never-fabricate
// check every WidgetWindow mapping below shares.
func windowUnknown(w Window) bool {
	return w.UtilizationPct == WindowUtilizationUnknown
}

// widgetWindow maps one Window to a *WidgetWindow, nil fields (not a
// fabricated zero) when w carries no real source.
func widgetWindow(w Window) *WidgetWindow {
	if windowUnknown(w) {
		return &WidgetWindow{}
	}
	pct := w.UtilizationPct
	resets := w.ResetsIn
	return &WidgetWindow{UtilizationPct: &pct, ResetsIn: &resets}
}

// widgetRowFromSlot maps one ProviderSlot to one WidgetRow. Label has no
// friendlier display-name source than the profile ref itself today (no
// nickname/display-name field exists on ProviderSlot) — Compose sets it
// to ref verbatim, an honest default Redact (widget_redact.go) may still
// override for the project/scope label rule.
func widgetRowFromSlot(ref string, slot ProviderSlot) WidgetRow {
	fiveHour := Window{UtilizationPct: WindowUtilizationUnknown}
	sevenDay := Window{UtilizationPct: WindowUtilizationUnknown}
	if b, ok := slot.Buckets[BucketInteractiveUsage]; ok {
		fiveHour = b.FiveHour
		sevenDay = b.SevenDay
	}
	return WidgetRow{
		Ref:            ref,
		Label:          ref,
		Kind:           "profile",
		FiveHour:       widgetWindow(fiveHour),
		SevenDay:       widgetWindow(sevenDay),
		State:          string(slot.State),
		ReauthRequired: slot.State == StateAuthRequired,
		UpdatedAt:      slot.UpdatedAt,
	}
}

// sortedKeys returns m's keys sorted ascending, for deterministic row
// order over a Go map (matching cmd/cascade/fleet_capacity.go's own
// identical sortedProviderNames precedent).
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Compose reduces snap plus the two in-process counts into a
// WidgetSnapshot. Pure: no I/O, no bare time.Now (now is the caller's
// injected clock reading, Art.7.3). generatedAt/seq are stamped by the
// caller (the daemon composition root owns the monotonic seq counter, not
// this stateless function).
//
// DISCLOSED GAP: Projects is always nil. No production source in this
// tree reports a project/scope inventory the widget could render as rows
// (the R-21.200 show_project_names toggle still governs whatever future
// ticket wires one — see widget_redact.go's Redact) — never fabricated
// here.
func Compose(snap FleetSnapshot, attentionCount int, activeJobsCount *int, generatedAt time.Time, seq uint64) WidgetSnapshot {
	rows := make([]WidgetRow, 0, len(snap.Providers))
	for _, ref := range sortedKeys(snap.Providers) {
		rows = append(rows, widgetRowFromSlot(ref, snap.Providers[ref]))
	}

	nodeSummaries := make([]NodePresenceSummary, 0, len(snap.Nodes))
	for _, id := range sortedKeys(snap.Nodes) {
		n := snap.Nodes[id]
		nodeSummaries = append(nodeSummaries, NodePresenceSummary{
			ID:        n.ID,
			Presence:  n.Presence,
			Reachable: n.Presence == PresenceReachable,
			TrustTier: n.TrustTier,
		})
	}

	return WidgetSnapshot{
		Rows:            rows,
		AttentionCount:  attentionCount,
		ActiveJobsCount: activeJobsCount,
		Nodes:           nodeSummaries,
		Projects:        nil,
		GeneratedAt:     generatedAt,
		Seq:             seq,
	}
}
