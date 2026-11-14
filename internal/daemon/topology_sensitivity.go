package daemon

// Purpose: wire topology.SensitivityTable to the conductor's one lane table
// (conductor.SensitivityAllows) at daemon composition, through an adapter from
// topology.LaneKind to conductor.LaneType. topology cannot import the conductor,
// so this is the one place the seam gets its value.
// Inputs: a topology lane kind, a tier and the restricted permit.
// Outputs: the conductor's decision; an unresolved or unknown kind is refused
// for every tier (the conductor's table admits public on an unresolved lane, so
// the adapter refuses it before asking).
// Constraints: PaidEvidenceCurrent stays unset (a paid domain stays capped at public).
// SPORT: internal/daemon (ADD, P1-TOP-02dw).

import (
	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/provider"
)

// laneTypeOf maps a topology lane kind onto the conductor lane type; ok is false
// for the unresolved kind and for any name outside the closed set.
func laneTypeOf(k topology.LaneKind) (lt conductor.LaneType, ok bool) {
	switch k {
	case topology.LaneKindControllerLocal:
		return conductor.LaneControllerLocal, true
	case topology.LaneKindExternalAPI:
		return conductor.LaneExternalAPI, true
	case topology.LaneKindUnresolved:
		return conductor.LaneUnresolved, false
	default:
		return conductor.LaneUnresolved, false
	}
}

// topologySensitivity is the SensitivityTable value: conductor.SensitivityAllows
// over the mapped lane type, and a refusal when the kind does not resolve.
func topologySensitivity(t provider.SensitivityTier, k topology.LaneKind, restrictedPermitted bool) bool {
	lt, ok := laneTypeOf(k)
	if !ok {
		return false
	}
	return conductor.SensitivityAllows(t, lt, restrictedPermitted)
}

// wireTopologySensitivity assigns the seam; the daemon's capacity step calls it
// once, before the lease floor can read a ceiling.
func wireTopologySensitivity() { topology.SensitivityTable = topologySensitivity }
