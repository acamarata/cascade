// Purpose: controller<->node version negotiation over the A-T6
//   internal/buildinfo ldflags stamp (§D-17/§D-33): a same-minor window
//   check with an actionable refusal, plus the controller-side warning
//   surfaced when an enrolled node's LAST-REPORTED stamp would fall
//   outside that window.
// Inputs: two buildinfo.Version-shaped strings ("v2.Y.Z", "dev", or
//   malformed attacker-controlled input — a node's reported stamp is
//   untrusted wire data, exactly like capability.go's CapabilityReport).
// Outputs: a NegotiationResult, or a typed KindUnsupported refusal naming
//   both versions when they fall outside the same-minor window.
// Constraints: FAIL CLOSED — an unparseable or "dev" version on EITHER
//   side refuses negotiation outright (12-QUALITY-CONSTITUTION Art.1:
//   never treat an unparseable version as "assume compatible"). A dev
//   build is real (buildinfo.Version's own documented default), so this
//   is not a stub path: it is the correct behavior for a stamp that
//   carries no comparable version.
// SPORT: internal/nodes NegotiateVersion/ADDED, ParseReleaseVersion/ADDED
//   (P1-E17-W4-S36-T5).

package nodes

import (
	"strconv"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ReleaseVersion is a parsed §D-33 artifact version (vMAJOR.MINOR.PATCH,
// distinct from the Go module's v0.Y.Z tag per buildinfo.go's own doc).
type ReleaseVersion struct {
	Major, Minor, Patch int
	Raw                 string
}

// ParseReleaseVersion parses a buildinfo.Version-shaped string. "dev" and
// any string that does not resolve to three non-negative integers after
// stripping a leading "v" are refused — never guessed at.
func ParseReleaseVersion(v string) (ReleaseVersion, error) {
	raw := v
	trimmed := strings.TrimPrefix(v, "v")
	parts := strings.SplitN(trimmed, ".", 3)
	if len(parts) != 3 {
		return ReleaseVersion{}, cascade.Newf(cascade.KindInvalidInput,
			"nodes: version %q is not in MAJOR.MINOR.PATCH form", raw)
	}
	nums := make([]int, 3)
	for i, p := range parts {
		// A patch component may carry a "-rc1"-style suffix; only the
		// leading integer run is significant to the same-minor window.
		digits := p
		for j := 0; j < len(digits); j++ {
			if digits[j] < '0' || digits[j] > '9' {
				digits = digits[:j]
				break
			}
		}
		n, err := strconv.Atoi(digits)
		if err != nil {
			return ReleaseVersion{}, cascade.Newf(cascade.KindInvalidInput,
				"nodes: version %q has a non-numeric component %q", raw, p)
		}
		nums[i] = n
	}
	return ReleaseVersion{Major: nums[0], Minor: nums[1], Patch: nums[2], Raw: raw}, nil
}

// SameMinorWindow reports whether a and b share the same major and minor
// version — the §D-17 negotiation window controller and node must agree
// within.
func (a ReleaseVersion) SameMinorWindow(b ReleaseVersion) bool {
	return a.Major == b.Major && a.Minor == b.Minor
}

// NegotiationResult is the outcome NegotiateVersion reports for a
// successfully parsed pair.
type NegotiationResult struct {
	Controller ReleaseVersion
	Node       ReleaseVersion
	// InWindow reports whether the two versions share a same-minor
	// window. NegotiateVersion never returns InWindow=false with a nil
	// error — an out-of-window pair is always also a refusal.
	InWindow bool
}

// ErrVersionOutOfWindow (KindUnsupported) is the actionable §D-17 refusal
// naming both versions.
func ErrVersionOutOfWindow(controllerVersion, nodeVersion string) error {
	return cascade.Newf(cascade.KindUnsupported,
		"nodes: version negotiation refused: controller %s and node %s are outside the same-minor window; upgrade the node to match the controller's minor version",
		controllerVersion, nodeVersion)
}

// NegotiateVersion parses controllerVersion and nodeVersion and enforces
// the same-minor window. A parse failure on either side, or a same-minor
// mismatch, refuses with a typed, actionable error — never a boolean
// "maybe compatible" the caller could ignore.
func NegotiateVersion(controllerVersion, nodeVersion string) (NegotiationResult, error) {
	cv, err := ParseReleaseVersion(controllerVersion)
	if err != nil {
		return NegotiationResult{}, cascade.Wrapf(cascade.KindInvalidInput, err,
			"nodes: version negotiation: controller version %q", controllerVersion)
	}
	nv, err := ParseReleaseVersion(nodeVersion)
	if err != nil {
		return NegotiationResult{}, cascade.Wrapf(cascade.KindInvalidInput, err,
			"nodes: version negotiation: node version %q", nodeVersion)
	}
	if !cv.SameMinorWindow(nv) {
		return NegotiationResult{}, ErrVersionOutOfWindow(controllerVersion, nodeVersion)
	}
	return NegotiationResult{Controller: cv, Node: nv, InWindow: true}, nil
}

// WouldFallOutOfWindow reports whether nodeVersion would already be
// outside controllerVersion's same-minor window — the §D-33 controller
// warning surfaced against an enrolled node's LAST-REPORTED capability
// stamp, distinct from NegotiateVersion's hard refusal on a live RPC:
// this is advisory (the controller keeps talking to the node and warns),
// never a refusal by itself. Malformed input on either side is reported
// as "would fall out of window" (true) — fail closed toward warning
// rather than silently skipping a node whose stamp cannot be read.
func WouldFallOutOfWindow(controllerVersion, nodeVersion string) bool {
	_, err := NegotiateVersion(controllerVersion, nodeVersion)
	return err != nil
}
