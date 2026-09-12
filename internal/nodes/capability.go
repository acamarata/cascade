// Purpose: the capability report a node carries in every heartbeat frame:
//
//	its declared capability set (consumed by S-37.T1's placement filter)
//	plus the K12 hardware-envelope preset (the v1 K12 concept; its
//	calibrated per-machine values are M/S-26.T4's domain — this ticket
//	REPORTS a classification, it does not calibrate one).
//
// Inputs: attacker-controlled wire bytes — a node reports its own
//
//	capabilities, so every field here is untrusted input from the
//	controller's point of view.
//
// Outputs: a validated CapabilityReport, or a typed fail-closed error for
//
//	anything malformed, oversized, or over-count.
//
// Constraints: CAPABILITY REPORTING IS ATTACKER-CONTROLLED INPUT (this
//
//	ticket's own security-boundary text): a reported capability is NEVER
//	read by anything in this package that makes an authorization or
//	trust_tier decision — trust.go's Rank/Satisfies/ValidateTier never
//	take a CapabilityReport as input, by construction (no such function
//	signature exists in this package), and this file's validation bounds
//	size and count so a node cannot exhaust the controller with an
//	unbounded report. M/S-26.T4 (internal/fleet/governor) already ships
//	HardwarePreset (minimal/balanced/performance) and calibrate(), but
//	calibrate is unexported and governor.go's Load() returns only the
//	resolved AdmissionConfig/LadderConfig, not the preset name it chose —
//	there is no exported seam this ticket can call to learn "which preset
//	did calibrate pick" without inventing new governor surface, which is
//	out of this ticket's files_scope. classifyK12 below therefore
//	reimplements the same two documented boundary constants
//	(internal/fleet/governor/calibrate.go's minimalMemCeilingBytes /
//	performanceMemFloorBytes, 24 GiB / 64 GiB) as this package's own
//	report-side classification — advisory only, never authoritative, and
//	never consumed for an admission decision (that stays governor's own
//	Load/calibrate path). See the ticket journal's CONTRADICTIONS section
//	for the full quote.
//
// SPORT: internal/nodes CapabilityReport/ADDED, K12Preset/ADDED
//
//	(P1-E17-W4-S36-T2).

package nodes

import "github.com/acamarata/cascade/pkg/cascade"

// K12 hardware-class memory boundaries, mirroring
// internal/fleet/governor/calibrate.go's own PROVISIONAL boundaries
// (17-INTAKE-2026-09-03-CHATGPT-V2-MONITORS.md row 22: a 16 GB LAN node,
// a 128 GB workstation) — see this file's package doc for why this ticket
// cannot call governor's own calibrate() directly.
const (
	k12MinimalMemCeilingBytes   = 24 * 1024 * 1024 * 1024
	k12PerformanceMemFloorBytes = 64 * 1024 * 1024 * 1024
	k12ClassMinimal             = "minimal"
	k12ClassBalanced            = "balanced"
	k12ClassPerformance         = "performance"
)

// K12Preset is the v1 K12 hardware-envelope concept a node reports about
// itself: a coarse device-class label plus the raw static hardware
// figures that produced it. It is advisory metadata only — nothing in
// this package or internal/fleet/governor reads it back to make an
// admission or trust decision; internal/fleet/governor.Load calibrates
// its own AdmissionConfig/LadderConfig from a local ResourceSnapshot on
// each machine independently.
type K12Preset struct {
	// Class is one of "minimal", "balanced", "performance" —
	// classifyK12's output, mirroring governor.HardwarePreset's three
	// string values so the two vocabularies never drift apart even
	// though this ticket cannot import the unexported classifier.
	Class string `json:"class"`
	// MemTotalBytes is the node's total physical memory, the sole axis
	// classifyK12 scores on (matching governor/calibrate.go's own
	// documented choice to score on static capacity, not momentary
	// load).
	MemTotalBytes uint64 `json:"mem_total_bytes"`
	// CPUCount is the node's logical CPU count, reported for operator
	// visibility. Not scored (governor's own ResourceSnapshot has no
	// CPU-count field either, per calibrate.go's documented deviation).
	CPUCount int `json:"cpu_count"`
}

// classifyK12 scores memTotalBytes against the same two boundaries
// internal/fleet/governor/calibrate.go uses, returning the matching class
// label.
func classifyK12(memTotalBytes uint64) string {
	switch {
	case memTotalBytes <= k12MinimalMemCeilingBytes:
		return k12ClassMinimal
	case memTotalBytes >= k12PerformanceMemFloorBytes:
		return k12ClassPerformance
	default:
		return k12ClassBalanced
	}
}

// NewK12Preset builds a K12Preset by classifying memTotalBytes.
func NewK12Preset(memTotalBytes uint64, cpuCount int) K12Preset {
	return K12Preset{
		Class:         classifyK12(memTotalBytes),
		MemTotalBytes: memTotalBytes,
		CPUCount:      cpuCount,
	}
}

// maxCapabilities bounds the number of capability strings one report may
// carry, so an unenrolled or misbehaving node cannot exhaust the
// controller's memory with an unbounded list.
const maxCapabilities = 64

// maxCapabilityLen bounds each individual capability string's length.
const maxCapabilityLen = 64

// CapabilityReport is the untrusted, node-reported capability set a
// heartbeat frame carries: 02-TARGET-STRUCTURE §Key contracts's
// need{browser, docker, 4hr_runtime} -> Conductor selects node vocabulary,
// plus the K12Preset. It is decoded and bound-validated by
// ValidateCapabilityReport before S-37.T1's (future) placement filter
// ever reads it; nothing in this file or package elevates a reported
// capability into a trust_tier or an authorization decision.
type CapabilityReport struct {
	// Capabilities is the node's declared capability set, e.g.
	// ["browser", "docker", "4hr_runtime"]. Free-form strings: the
	// closed vocabulary, if any, is S-37.T1 placement's concern, not
	// this wire format's.
	Capabilities []string `json:"capabilities"`
	// K12 is the node's self-reported hardware-envelope classification.
	K12 K12Preset `json:"k12"`
	// BuildVersion is the node's own internal/buildinfo.Version stamp
	// (§D-17/§D-33), carried here so version.go's controller-side
	// same-minor-window warning (WouldFallOutOfWindow) can read a node's
	// last-reported version without a separate wire round trip. Untrusted
	// wire input like every other field in this struct: version.go's
	// parser fails closed on anything malformed, never on this bound
	// check, which only guards against an oversized string.
	BuildVersion string `json:"build_version,omitempty"`
}

// maxBuildVersionLen bounds CapabilityReport.BuildVersion, mirroring
// maxCapabilityLen's role for the Capabilities slice.
const maxBuildVersionLen = 32

// ValidateCapabilityReport fail-closed-validates an untrusted
// CapabilityReport: the capability list must not exceed maxCapabilities
// entries, no entry may exceed maxCapabilityLen bytes or be empty, and no
// entry may repeat. A report that fails any of these bounds is refused in
// full — this function never truncates or silently drops entries to make
// an oversized report fit, since a partially-accepted report would let an
// attacker learn the truncation boundary.
func ValidateCapabilityReport(cr CapabilityReport) error {
	if len(cr.Capabilities) > maxCapabilities {
		return cascade.Newf(cascade.KindInvalidInput,
			"nodes: capability report declares %d capabilities, exceeding the %d limit", len(cr.Capabilities), maxCapabilities)
	}
	seen := make(map[string]struct{}, len(cr.Capabilities))
	for _, c := range cr.Capabilities {
		if c == "" {
			return cascade.New(cascade.KindInvalidInput, "nodes: capability report contains an empty capability string")
		}
		if len(c) > maxCapabilityLen {
			return cascade.Newf(cascade.KindInvalidInput,
				"nodes: capability %q exceeds the %d-byte limit", c, maxCapabilityLen)
		}
		if _, dup := seen[c]; dup {
			return cascade.Newf(cascade.KindInvalidInput, "nodes: capability report declares %q more than once", c)
		}
		seen[c] = struct{}{}
	}
	if cr.K12.Class != k12ClassMinimal && cr.K12.Class != k12ClassBalanced && cr.K12.Class != k12ClassPerformance {
		return cascade.Newf(cascade.KindInvalidInput, "nodes: capability report has unrecognized k12 class %q", cr.K12.Class)
	}
	if len(cr.BuildVersion) > maxBuildVersionLen {
		return cascade.Newf(cascade.KindInvalidInput, "nodes: capability report build_version exceeds the %d-byte limit", maxBuildVersionLen)
	}
	return nil
}

// HasCapability reports whether cr declares name, for a future placement
// filter's need{...} check. It is a pure membership test: it never
// consults, widens, or otherwise touches a trust_tier.
func HasCapability(cr CapabilityReport, name string) bool {
	for _, c := range cr.Capabilities {
		if c == name {
			return true
		}
	}
	return false
}
