package nodes

import (
	"sort"
	"strconv"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: turn "no node can run this" into an error an operator can act
//
//	on, naming why each candidate was excluded.
//
// Inputs: the requirement that found no home, and one Exclusion per
//
//	rejected candidate.
//
// Outputs: a typed cascade error.
// Constraints: an empty eligible set is ALWAYS an error. There is no
//
//	silent fallback to the controller machine: work classified to run
//	somewhere specific must fail loudly when it cannot, rather than running
//	somewhere else.
//
// SPORT: internal/nodes placement error mapping (ADD) — P1-E17-W4-S37-T1.

// ExclusionReason names why one candidate was rejected. It is a closed set
// so a caller can group and count reasons rather than parse a message.
type ExclusionReason string

const (
	// ReasonLocalOnlyWork is set when the work may only run on the controller machine.
	ReasonLocalOnlyWork ExclusionReason = "local-only-work"
	// ReasonTierTooLow is set when the node's trust tier does not clear the work's
	// sensitivity, or is not a tier this build recognizes.
	ReasonTierTooLow ExclusionReason = "trust-tier-too-low"
	// ReasonDrained is set when an operator marked the node as not accepting work.
	ReasonDrained ExclusionReason = "drained"
	// ReasonNotReachable is set when the node's presence is anything but reachable.
	ReasonNotReachable ExclusionReason = "not-reachable"
	// ReasonNotConnected is set when no tunnel is up to the node.
	ReasonNotConnected ExclusionReason = "not-connected"
	// ReasonMissingCapability is set when the node does not report every capability
	// the work requires.
	ReasonMissingCapability ExclusionReason = "missing-capability"
)

// Exclusion records one rejected candidate and why.
type Exclusion struct {
	// NodeID is the rejected node.
	NodeID string
	// Reason is the closed-set cause.
	Reason ExclusionReason
	// Detail is the human-readable specifics, e.g. which capabilities were
	// missing.
	Detail string
}

// ErrNoEligibleNode builds the placement failure.
//
// The message leads with the aggregate ("4 enrolled nodes, none eligible")
// and then the reason breakdown, because that is the shape of the question
// an operator actually has: whether this is one broken node or a fleet-wide
// condition like everything being drained. Per-node detail follows, bounded
// so a large fleet produces a readable error rather than a wall of text.
//
// Kind is KindUnavailable: the request was well formed and permitted, there
// is simply nowhere to run it right now. It is deliberately not
// KindNotFound (the nodes exist) and not a permission kind (a tier
// exclusion is one possible cause among several, not the outcome's
// meaning).
func ErrNoEligibleNode(req Requirement, exclusions []Exclusion) error {
	if len(exclusions) == 0 {
		return cascade.New(cascade.KindUnavailable,
			"nodes: no enrolled node is eligible: the fleet has no enrolled nodes")
	}
	msg := "nodes: no enrolled node is eligible for this work (" +
		strconv.Itoa(len(exclusions)) + " considered, 0 eligible)"
	if len(req.Capabilities) > 0 {
		msg += "; required capabilities: " + joinNames(req.Capabilities)
	}
	msg += "; by reason: " + summarizeReasons(exclusions)
	if detail := perNodeDetail(exclusions); detail != "" {
		msg += "; " + detail
	}
	return cascade.New(cascade.KindUnavailable, msg)
}

// maxPerNodeDetail bounds how many individual nodes the message names, so
// a hundred-node fleet does not produce a hundred-clause error.
const maxPerNodeDetail = 5

// summarizeReasons counts exclusions by reason, rendered in a stable order
// (most common first, then alphabetical) so the same fleet state always
// produces the same message.
func summarizeReasons(exclusions []Exclusion) string {
	counts := map[ExclusionReason]int{}
	for _, e := range exclusions {
		counts[e.Reason]++
	}
	reasons := make([]ExclusionReason, 0, len(counts))
	for r := range counts {
		reasons = append(reasons, r)
	}
	sort.Slice(reasons, func(i, j int) bool {
		if counts[reasons[i]] != counts[reasons[j]] {
			return counts[reasons[i]] > counts[reasons[j]]
		}
		return reasons[i] < reasons[j]
	})
	out := ""
	for i, r := range reasons {
		if i > 0 {
			out += ", "
		}
		out += strconv.Itoa(counts[r]) + " " + string(r)
	}
	return out
}

// perNodeDetail renders up to maxPerNodeDetail individual exclusions,
// saying explicitly how many were omitted rather than truncating silently.
func perNodeDetail(exclusions []Exclusion) string {
	shown := exclusions
	omitted := 0
	if len(shown) > maxPerNodeDetail {
		omitted = len(shown) - maxPerNodeDetail
		shown = shown[:maxPerNodeDetail]
	}
	out := ""
	for i, e := range shown {
		if i > 0 {
			out += "; "
		}
		out += e.NodeID + ": " + e.Detail
	}
	if omitted > 0 {
		out += "; and " + strconv.Itoa(omitted) + " more"
	}
	return out
}
