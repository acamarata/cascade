// Package pbd (validate_deps.go): Purpose: formatting for a ticket's
//
//	dependency-edge violations (dangling targets, tombstone targets,
//	cycles) — as opposed to a ticket's own identity/sequencing bookkeeping
//	(validate_ids.go owns those).
//
// Inputs: a pews.Violation whose Kind is one of depViolationKinds.
// Outputs: one human-readable report line.
// Constraints: pkg/** and internal/pews imports only (Art.10.2).
// SPORT: plugins/pbd validate_deps (ADD) — P1-E14-W3-S28-T2.
package pbd

import (
	"fmt"

	"github.com/acamarata/cascade/plugins/pbd/internal/pews"
)

// depViolationKinds is the set of violation kinds this file formats.
var depViolationKinds = map[pews.ViolationKind]bool{
	pews.ViolationDanglingDep:  true,
	pews.ViolationTombstoneDep: true,
	pews.ViolationCycle:        true,
}

// formatDepViolation renders one dependency-class violation the same way
// formatIDViolation renders an id-class one.
func formatDepViolation(v pews.Violation) string {
	if v.Path != "" {
		return fmt.Sprintf("[%s] %s (%s)", v.Kind, v.Message, v.Path)
	}
	return fmt.Sprintf("[%s] %s", v.Kind, v.Message)
}
