package daemon

// Purpose (this file): the change key the emit path dedups on
// (status_widget_sse.go): two snapshots with equal keys are the same state
// of the world, so the second publishes nothing.
//
// Inputs: a redacted capacity.WidgetSnapshot.
// Outputs: the canonical JSON of that snapshot minus seq, generated_at and
// every updated_at, with each reset countdown replaced by the absolute
// instant it counts down to.
// Constraints: resets_in is derived as ResetEstimate minus generated_at, so
// it shrinks on every tick while the lane has not changed; keying on the
// countdown itself would publish a frame per tick for any row with a reset,
// defeating the dedup. generated_at plus resets_in is the lane's own
// ResetEstimate (stable until the lane changes), so keying on that detects
// a changed or cleared estimate and nothing else. This is the one place the
// key departs from the literal "snapshot minus seq, generated_at and
// updated_at", and it departs only for resets_in.
//
// SPORT: daemon.status_widget (ADD, P1-WID-08).

import (
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/internal/fleet/capacity"
)

// widgetKeyWindow is one window as the key sees it.
type widgetKeyWindow struct {
	UtilizationPct *float64   `json:"utilization_pct"`
	ResetAt        *time.Time `json:"reset_at"`
}

// widgetKeyRow is one row as the key sees it: no updated_at.
type widgetKeyRow struct {
	Ref            string          `json:"ref"`
	Label          string          `json:"label"`
	Kind           string          `json:"kind"`
	FiveHour       widgetKeyWindow `json:"five_hour"`
	SevenDay       widgetKeyWindow `json:"seven_day"`
	State          string          `json:"state"`
	ReauthRequired bool            `json:"reauth_required"`
}

// widgetKeyBody is the whole key document.
type widgetKeyBody struct {
	Rows            []widgetKeyRow                 `json:"rows"`
	AttentionCount  int                            `json:"attention_count"`
	ActiveJobsCount *int                           `json:"active_jobs_count"`
	Nodes           []capacity.NodePresenceSummary `json:"nodes"`
	Projects        []capacity.ProjectRow          `json:"projects"`
}

// keyWindow maps w, composed at generatedAt, to its key form.
func keyWindow(w *capacity.WidgetWindow, generatedAt time.Time) widgetKeyWindow {
	if w == nil {
		return widgetKeyWindow{}
	}
	out := widgetKeyWindow{UtilizationPct: w.UtilizationPct}
	if w.ResetsIn != nil {
		at := generatedAt.Add(*w.ResetsIn).UTC()
		out.ResetAt = &at
	}
	return out
}

// widgetChangeKey returns the canonical change key of snap.
func widgetChangeKey(snap capacity.WidgetSnapshot) ([]byte, error) {
	body := widgetKeyBody{
		Rows:            make([]widgetKeyRow, 0, len(snap.Rows)),
		AttentionCount:  snap.AttentionCount,
		ActiveJobsCount: snap.ActiveJobsCount,
		Nodes:           snap.Nodes,
		Projects:        make([]capacity.ProjectRow, len(snap.Projects)),
	}
	for _, r := range snap.Rows {
		body.Rows = append(body.Rows, widgetKeyRow{
			Ref: r.Ref, Label: r.Label, Kind: r.Kind, State: r.State, ReauthRequired: r.ReauthRequired,
			FiveHour: keyWindow(r.FiveHour, snap.GeneratedAt), SevenDay: keyWindow(r.SevenDay, snap.GeneratedAt),
		})
	}
	for i, p := range snap.Projects {
		p.UpdatedAt = time.Time{}
		body.Projects[i] = p
	}
	return json.Marshal(body)
}
