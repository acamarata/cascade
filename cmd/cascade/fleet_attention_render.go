// Purpose: `cascade fleet attention`'s table-render types, split out of
//
//	fleet_attention.go purely to keep that file focused on command
//	construction, mirroring fleet_journal.go's own row-rendering section
//	kept in the same file there only because it fit under 300 lines —
//	here it does not once the CONTRACT DEVIATION doc comment is
//	included, so it is its own file instead.
//
// SPORT: cmd/cascade/fleet (ADD, per T-1 sport_updates; attention render
//
//	half).
package main

import (
	"bytes"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/runtime"
)

// attentionRow is one rendered list-view row: id/kind/source/age/priority
// (the ticket's own column set).
type attentionRow struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Source   string `json:"source_ref"`
	Age      string `json:"age"`
	Priority int    `json:"priority"`
}

// newAttentionRow builds one row from a real supervision.AttentionItem.
func newAttentionRow(item supervision.AttentionItem) attentionRow {
	return attentionRow{
		ID:       item.ID,
		Kind:     string(item.Kind),
		Source:   item.SourceRef,
		Age:      formatElapsed(item.CreatedAt/1000, runtime.NewSystemClock()),
		Priority: item.Priority,
	}
}

// attentionRows wraps []attentionRow with a human table String().
type attentionRows []attentionRow

func attentionRowsFrom(items []supervision.AttentionItem) attentionRows {
	rows := make(attentionRows, 0, len(items))
	for _, item := range items {
		rows = append(rows, newAttentionRow(item))
	}
	return rows
}

// String renders the table view.
func (rows attentionRows) String() string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "ID\tKIND\tSOURCE\tAGE\tPRIORITY\n")
	for _, r := range rows {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\n", r.ID, r.Kind, r.Source, r.Age, r.Priority)
	}
	_ = tw.Flush()
	return strings.TrimRight(buf.String(), "\n")
}

// attentionRowFrom wraps a single item for ack's result (same shape as a
// list row, wrapped so it still renders as a one-row table on non-JSON
// output).
func attentionRowFrom(item supervision.AttentionItem) attentionRows {
	return attentionRows{newAttentionRow(item)}
}

// attentionDetail is `open`'s full-detail render: every AttentionItem
// field, unlike list's five-column summary.
type attentionDetail struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	SourceRef string  `json:"source_ref"`
	ScopeKind string  `json:"scope_kind"`
	ScopeID   string  `json:"scope_id"`
	Priority  int     `json:"priority"`
	CreatedAt string  `json:"created_at"`
	AckedAt   *string `json:"acked_at,omitempty"`
}

func attentionDetailFrom(item supervision.AttentionItem) attentionDetail {
	d := attentionDetail{
		ID:        item.ID,
		Kind:      string(item.Kind),
		SourceRef: item.SourceRef,
		ScopeKind: string(item.ScopeRef.Kind),
		ScopeID:   item.ScopeRef.ID,
		Priority:  item.Priority,
		CreatedAt: time.UnixMilli(item.CreatedAt).Format(time.RFC3339Nano),
	}
	if item.AckedAt != nil {
		s := time.UnixMilli(*item.AckedAt).Format(time.RFC3339Nano)
		d.AckedAt = &s
	}
	return d
}

// String renders the detail view as a simple field-per-line block, so
// `open` reads like a record dump rather than a truncated table row.
func (d attentionDetail) String() string {
	var buf strings.Builder
	fmt.Fprintf(&buf, "id:          %s\n", d.ID)
	fmt.Fprintf(&buf, "kind:        %s\n", d.Kind)
	fmt.Fprintf(&buf, "source_ref:  %s\n", d.SourceRef)
	fmt.Fprintf(&buf, "scope:       %s/%s\n", d.ScopeKind, d.ScopeID)
	fmt.Fprintf(&buf, "priority:    %d\n", d.Priority)
	fmt.Fprintf(&buf, "created_at:  %s\n", d.CreatedAt)
	if d.AckedAt != nil {
		fmt.Fprintf(&buf, "acked_at:    %s\n", *d.AckedAt)
	} else {
		fmt.Fprintf(&buf, "acked_at:    -\n")
	}
	return strings.TrimRight(buf.String(), "\n")
}
