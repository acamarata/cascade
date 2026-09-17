package nodes

// Purpose: decide which enrolled nodes are ELIGIBLE to run a unit of work,
//
//	given its requirement set and the fleet's current state.
//
// Inputs: a Requirement (the need{browser, docker, 4hr_runtime} vocabulary
//
//	plus the work's resolved sensitivity), one Candidate per enrolled node
//	(its DeviceRecord and its last validated CapabilityReport), and a
//	lookup for live tunnel state.
//
// Outputs: the eligible subset, or a typed, actionable error naming why
//
//	every candidate was excluded.
//
// Constraints: this is ELIGIBILITY only. Selection among eligible nodes —
//
//	cost, health, privacy and lane scoring — belongs to the conductor's
//	router (K/S-22.T2); this engine deliberately returns a set rather than
//	a choice. It is a pure decision over its inputs: no clock, no I/O, no
//	network, so the same inputs always yield the same answer. The task-class
//	vocabulary a report carries is ROUTER input (advisory lane affinity) and
//	is deliberately NOT a placement dimension here.
//
// SPORT: internal/nodes placement (ADD) — P1-E17-W4-S37-T1.

// Requirement is one unit of work's placement demand.
type Requirement struct {
	// Capabilities are the capability names a node must ALL report to be
	// eligible, e.g. ["browser", "docker"]. An empty set demands nothing
	// of a node's capabilities; it does not bypass any other filter.
	Capabilities []string
	// Sensitivity is the work's resolved sensitivity. The zero value is
	// deliberately not "no restriction": see Sensitivity's own doc, it
	// resolves most restrictively.
	Sensitivity Sensitivity
}

// Candidate pairs an enrolled node's record with the capability report it
// last sent. The report is the already-validated S-36.T2 type: this engine
// re-derives nothing from the wire and elevates no reported capability into
// a trust decision.
type Candidate struct {
	// Record is the node's enrolled device record.
	Record DeviceRecord
	// Report is the node's last validated capability report.
	Report CapabilityReport
}

// TunnelStateLookup reports the live tunnel state for a node id. It is a
// seam rather than a field on DeviceRecord because connection state is
// live, per-process and not persisted: the record says a node is enrolled,
// the tunnel says the controller can reach it right now.
//
// A nil lookup is treated as "no node is connected" rather than "every node
// is connected" — an engine wired without its connection source must place
// nothing, never everything.
type TunnelStateLookup func(nodeID string) TunnelState

// Engine decides placement eligibility. The zero value is usable and
// places nothing, because its nil TunnelStateLookup reports no node as
// connected; construct it with the real lookup to place work.
type Engine struct {
	// Tunnels reports live tunnel state per node id.
	Tunnels TunnelStateLookup
}

// Eligible returns every candidate that clears all four filters, in the
// order candidates were supplied so the router sees a deterministic set.
//
// The filters are applied most-restrictive-first — sensitivity, then trust
// tier, then fleet state, then capability match — so the recorded exclusion
// reason is the most fundamental one true of that node rather than whichever
// check happened to run first. A node missing a capability AND drained is
// reported as drained, because that is the condition an operator would act
// on.
//
// An empty result is an error, never an empty slice with a nil error: the
// caller must not be able to treat "nowhere to run this" as success and
// quietly fall back to the controller machine.
func (e Engine) Eligible(req Requirement, candidates []Candidate) ([]DeviceRecord, error) {
	eligible := make([]DeviceRecord, 0, len(candidates))
	exclusions := make([]Exclusion, 0, len(candidates))

	for _, c := range candidates {
		if reason, detail, excluded := e.exclude(req, c); excluded {
			exclusions = append(exclusions, Exclusion{NodeID: c.Record.NodeID, Reason: reason, Detail: detail})
			continue
		}
		eligible = append(eligible, c.Record)
	}

	if len(eligible) == 0 {
		return nil, ErrNoEligibleNode(req, exclusions)
	}
	return eligible, nil
}

// exclude reports whether c fails any filter for req, and why. It returns
// the FIRST failure in most-restrictive-first order; see Eligible's doc for
// why that order is the useful one.
func (e Engine) exclude(req Requirement, c Candidate) (reason ExclusionReason, detail string, excluded bool) {
	if reason, detail, bad := excludedByTrust(req.Sensitivity, c.Record.Tier); bad {
		return reason, detail, true
	}
	if reason, detail, bad := e.excludedByFleetState(c.Record); bad {
		return reason, detail, true
	}
	if missing := missingCapabilities(req.Capabilities, c.Report.Capabilities); len(missing) > 0 {
		return ReasonMissingCapability, joinNames(missing), true
	}
	return "", "", false
}

// missingCapabilities returns every required capability absent from
// reported, preserving the required order so the message is stable.
//
// Matching is exact: a capability is a name from a shared vocabulary, not a
// prefix or a pattern, and a node reporting "docker-ce" does not satisfy a
// requirement for "docker".
func missingCapabilities(required, reported []string) []string {
	if len(required) == 0 {
		return nil
	}
	have := make(map[string]bool, len(reported))
	for _, capability := range reported {
		have[capability] = true
	}
	var missing []string
	for _, capability := range required {
		if !have[capability] {
			missing = append(missing, capability)
		}
	}
	return missing
}

// joinNames renders names for an error detail, comma-separated.
func joinNames(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}

// CandidatesFrom pairs each enrolled record with the capability report
// that record itself carries (DeviceRecord.LastReport — written only by
// ProcessHeartbeat, and only after the frame's signature verified).
//
// It exists so a caller does not have to know HOW a node's capabilities
// are obtained in order to ask whether that node is eligible. A record
// whose node has never sent a verified heartbeat carries the zero report,
// which advertises nothing and therefore satisfies no capability
// requirement — the fail-closed reading, reached without a special case.
func CandidatesFrom(records []DeviceRecord) []Candidate {
	candidates := make([]Candidate, 0, len(records))
	for _, rec := range records {
		candidates = append(candidates, Candidate{Record: rec, Report: rec.LastReport})
	}
	return candidates
}
