// Package pbd (validate_ids.go): Purpose: formatting for a ticket's own
//
//	identity/sequencing/tombstone-bookkeeping violations — everything
//	about ONE ticket's id, as opposed to its dependency edges
//	(validate_deps.go owns those).
//
// Inputs: a pews.Violation whose Kind is one of idViolationKinds.
// Outputs: one human-readable report line.
// Constraints: pkg/** and internal/pews imports only (Art.10.2).
// SPORT: plugins/pbd validate_ids (ADD) — P1-E14-W3-S28-T2.
package pbd

import (
	"fmt"

	"github.com/acamarata/cascade/plugins/pbd/internal/pews"
)

// idViolationKinds is the set of violation kinds this file formats.
var idViolationKinds = map[pews.ViolationKind]bool{
	pews.ViolationIdentityMismatch: true,
	pews.ViolationDuplicateID:      true,
	pews.ViolationGap:              true,
	pews.ViolationTombstoneLive:    true,
	pews.ViolationDuplicateTomb:    true,
}

// formatIDViolation renders one id-class violation as a single report
// line, prefixed with its kind so violations of the same kind group
// naturally when several appear in one report.
func formatIDViolation(v pews.Violation) string {
	if v.Path != "" {
		return fmt.Sprintf("[%s] %s (%s)", v.Kind, v.Message, v.Path)
	}
	return fmt.Sprintf("[%s] %s", v.Kind, v.Message)
}
