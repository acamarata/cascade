// Purpose: this package's sentinel errors, each wrapping exactly one
//
//	frozen pkg/cascade.Kind from the A-T7 taxonomy (R-14.2) -- no new
//	taxonomy kind is defined here (06-FORGE-SPEC §2).
//
// Inputs: none. Outputs: *cascade.Error values and the Violation type
//
//	invariants.Validate reports.
//
// Constraints: never invent a cascade.Kind; every error a caller of this
//
//	package sees traces back to one of the fourteen frozen kinds.
//
// SPORT: fleet/topology/errors/ADD (P1-E40-W9-S77-T1).

package topology

import "github.com/acamarata/cascade/pkg/cascade"

// Sentinel errors, each wrapping exactly one frozen pkg/cascade.Kind.
var (
	// ErrTopologyInvariant reports that a write or reconcile would violate
	// one of the R-21.24/R-21.107 topology invariants.
	ErrTopologyInvariant = cascade.New(cascade.KindInvalidInput, "topology: invariant violation")
	// ErrTopologyNotFound reports that a named topology row does not exist.
	ErrTopologyNotFound = cascade.New(cascade.KindNotFound, "topology: not found")
	// ErrTopologyConflict reports a topology state conflict.
	ErrTopologyConflict = cascade.New(cascade.KindConflict, "topology: conflict")
	// ErrLaneRetired is returned when a caller attempts a NEW reservation
	// against a retired lane (R-21.124). In-flight reservations run to
	// their terminal state; only a new attempt is refused.
	ErrLaneRetired = cascade.New(cascade.KindConflict, "topology: lane is retired")
	// ErrConfigRange reports a config value outside its documented valid
	// range (R-21.117's ValidateReserve). The caller still receives a
	// safely-clamped value alongside this error -- see ValidateReserve's
	// doc comment for why an out-of-range config can never permanently
	// disable a lane.
	ErrConfigRange = cascade.New(cascade.KindInvalidInput, "topology: config value outside its valid range")
)

// Violation carries one invariant failure: the invariant's name, the
// entity kind and row id that offended it, and a human-readable detail.
// invariants.Validate returns a []Violation rather than stopping at the
// first failure, so a single Reconcile pass or store write reports every
// problem at once.
type Violation struct {
	Invariant string
	Entity    string
	RowID     string
	Detail    string
}

// newInvariantErr wraps ErrTopologyInvariant with a single Violation's
// detail, for call sites (e.g. BaseShadowPrice) that report exactly one
// violation as an error rather than a []Violation slice.
func newInvariantErr(entity, rowID, detail string) error {
	return cascade.Wrapf(cascade.KindInvalidInput, ErrTopologyInvariant, "%s %q: %s", entity, rowID, detail)
}

// newNotFoundErr wraps ErrTopologyNotFound naming the missing entity/id.
func newNotFoundErr(entity, rowID string) error {
	return cascade.Wrapf(cascade.KindNotFound, ErrTopologyNotFound, "%s %q not found", entity, rowID)
}
