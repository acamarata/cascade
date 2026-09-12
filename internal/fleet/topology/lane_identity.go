// Purpose: R-21.124's derived Lane.ID -- deterministic so every writer,
//
//	including Reconcile, upserts by the same key and a retired-then-
//	returned lane keeps its reservation/scheduler_decision/Beta-posterior
//	references.
//
// Inputs: the five components LaneID hashes. Outputs: a LaneID.
// Constraints: never generated fresh/random; pinned by
//
//	testdata/lane_identity.golden.json.
//
// SPORT: fleet/topology/lane_identity/ADD (P1-E40-W9-S77-T1).

package topology

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// laneIDHexLen is the number of hex characters LaneID keeps from the full
// sha256 digest (R-21.124: "the first 16 hex characters").
const laneIDHexLen = 16

// DeriveLaneID computes the R-21.124 derived Lane.ID: the first sixteen
// hex characters of sha256 over runtimeProfileRef, quotaDomainRef,
// canonicalOrModelID, effort and interactionClass, joined by "|". Callers
// pass model_identity.canonical_id as canonicalOrModelID when it is
// non-empty and model_id otherwise (canonicalOrModelIDFor does this
// selection for a Lane).
func DeriveLaneID(runtimeProfileRef, quotaDomainRef, canonicalOrModelID, effort, interactionClass string) LaneID {
	joined := strings.Join([]string{
		runtimeProfileRef, quotaDomainRef, canonicalOrModelID, effort, interactionClass,
	}, "|")
	sum := sha256.Sum256([]byte(joined))
	return LaneID(hex.EncodeToString(sum[:])[:laneIDHexLen])
}

// canonicalOrModelIDFor selects the third DeriveLaneID component for lane:
// model_identity.canonical_id when non-empty, otherwise model_id
// (R-21.124's own wording).
func canonicalOrModelIDFor(canonicalID, modelID string) string {
	if canonicalID != "" {
		return canonicalID
	}
	return modelID
}

// LaneIDFor derives lane's R-21.124 identity from its own fields, using
// canonicalOrModelIDFor's selection rule.
func LaneIDFor(runtimeProfileRef RuntimeProfileID, quotaDomainRef DomainID, canonicalID, modelID string, effort Effort, interactionClass InteractionClass) LaneID {
	return DeriveLaneID(
		string(runtimeProfileRef),
		string(quotaDomainRef),
		canonicalOrModelIDFor(canonicalID, modelID),
		string(effort),
		string(interactionClass),
	)
}
