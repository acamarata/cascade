// Package coverage (this file): the gap computation half of the
// inventory-coverage-matrix gate. covmatrix.go loads the three sources
// (inventory rows, ticket tree, deferrals); this file maps one against
// the other two and names what is left over.
package coverage

import (
	"fmt"
	"strings"
)

// RowCoverage is one inventory row's resolution result.
type RowCoverage struct {
	Row       InventoryRow
	TicketIDs []string // citations that resolved to a real ticket file
	Deferrals []string // citations that resolved to a real deferral id
}

// Covered reports whether Row resolved to at least one ticket or
// deferral.
func (c RowCoverage) Covered() bool {
	return len(c.TicketIDs) > 0 || len(c.Deferrals) > 0
}

// InventoryGapReport lists every inventory row that resolved to neither
// a real ticket citation nor a real deferral id.
type InventoryGapReport struct {
	Gaps []InventoryRow
}

// Empty reports whether the report names zero gaps.
func (r InventoryGapReport) Empty() bool { return len(r.Gaps) == 0 }

// String renders one gap per line, for gate failure output.
func (r InventoryGapReport) String() string {
	var b strings.Builder
	for _, row := range r.Gaps {
		fmt.Fprintf(&b, "  - %s: %s\n", row.RowID(), strings.TrimSpace(row.Text))
	}
	return strings.TrimRight(b.String(), "\n")
}

// BuildCoverageMatrix resolves every row in rows against tickets and
// deferrals: a citation extracted from the row's text (ExtractCitations)
// either matches a known ticket id in tickets, matches a known deferral
// id in deferrals, or resolves against root's real epic tree via
// ResolveTicketCitation for a ticket id that ExtractCitations found but
// LoadTicketTree's snapshot did not carry (e.g. a caller passed a partial
// ticket slice in a test) — the live gate always passes the same root
// and the same LoadTicketTree result, so this fallback exists for
// correctness under a partial slice, not as its primary path.
//
// A row with no extractable citation at all is a gap outright; no
// network, no fuzzy matching, no partial credit.
func BuildCoverageMatrix(root string, rows []InventoryRow, tickets []TicketRecord, deferrals []DeferralEntry) ([]RowCoverage, error) {
	// tickets is accepted (and required non-empty by the live gate's own
	// LoadTicketTree call) so a caller cannot pass an inventory-only
	// view and get a matrix that never even tried to resolve a ticket;
	// resolution itself walks root directly (ResolveTicketCitation),
	// which is the source of truth tickets was snapshotted from.
	if len(tickets) == 0 {
		return nil, fmt.Errorf("coverage: BuildCoverageMatrix called with zero tickets (fail closed)")
	}
	knownDeferrals := make(map[string]bool, len(deferrals))
	for _, d := range deferrals {
		knownDeferrals[d.ID] = true
	}

	out := make([]RowCoverage, 0, len(rows))
	for _, row := range rows {
		cov := RowCoverage{Row: row}
		for _, citation := range ExtractCitations(row.Text) {
			if knownDeferrals[citation] {
				cov.Deferrals = append(cov.Deferrals, citation)
				continue
			}
			resolved, err := ResolveTicketCitation(root, citation)
			if err != nil {
				return nil, err
			}
			if resolved {
				cov.TicketIDs = append(cov.TicketIDs, citation)
			}
		}
		out = append(out, cov)
	}
	return out, nil
}

// GapReport reduces a coverage slice to the rows that resolved to
// nothing.
func GapReport(coverage []RowCoverage) InventoryGapReport {
	var report InventoryGapReport
	for _, c := range coverage {
		if !c.Covered() {
			report.Gaps = append(report.Gaps, c.Row)
		}
	}
	return report
}
