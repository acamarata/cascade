// Purpose: the "nodes" doctor.Check (R-14.91): reports fleet liveness
//
//	sourced from the heartbeat-updated device records, so `cascade doctor`
//	surfaces node health the same way every other subsystem does.
//
// Inputs: a *RecordStore and a Clock, injected at construction.
// Outputs: doctor.CheckResult summarizing every enrolled node's derived
//
//	Liveness (liveness.go).
//
// Constraints: Art.1 — a check that cannot verify its subject reports
//
//	StatusError, never a silent StatusOK; an empty fleet (no device
//	records at all) is a legitimate state, reported as StatusOK with an
//	explanatory message, not an error. Registration into the composition
//	root's productionCheckRegistry (cmd/cascade/doctor_mounts.go) is a
//	file this ticket's files_scope does not include (change: lists only
//	cmd/cascade/root.go and docs/reference/nodes.md) — see the ticket
//	journal's CONTRADICTIONS section for the full quote of the gap this
//	leaves, mirroring config.go's identical documented precedent for the
//	same kind of composition-root gap.
//
// SPORT: internal/nodes HealthCheck/ADDED (P1-E17-W4-S36-T2).

package nodes

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/doctor"
)

// NodesCheckName is this check's stable registry name — the
// `cascade doctor` row label and, per R-14.91's contract text, the
// selector `doctor --nodes` would filter on if/when the composition root
// wires per-check CLI selectors (it does not today; see this file's
// package doc).
const NodesCheckName = "nodes"

// HealthCheck is the doctor.Check implementation: it lists every
// enrolled device record and derives each one's current Liveness.
type HealthCheck struct {
	records *RecordStore
	clock   Clock
	timeout time.Duration
}

// NewHealthCheck constructs the check over records, deriving
// liveness at clock's current instant against timeout (0 means
// DefaultHeartbeatTimeout).
func NewHealthCheck(records *RecordStore, clock Clock, timeout time.Duration) doctor.Check {
	if timeout <= 0 {
		timeout = DefaultHeartbeatTimeout
	}
	return HealthCheck{records: records, clock: clock, timeout: timeout}
}

// Name returns the check's stable registry name.
func (HealthCheck) Name() string { return NodesCheckName }

// Describe returns the check's human-readable one-liner.
func (HealthCheck) Describe() string {
	return "reports enrolled node liveness from the heartbeat-updated device records"
}

// Metadata declares this check as ordinary (fleet liveness is not a
// first-run concern — a fresh install has no enrolled nodes at all) and
// not fixable: there is no remediation doctor could apply to a node that
// stopped heartbeating, only an operator action (re-enroll, investigate
// the node).
func (HealthCheck) Metadata() doctor.CheckMeta {
	return doctor.CheckMeta{FirstRun: false, Fixable: false}
}

// Fix always returns ErrCheckNotFixable, per Metadata().Fixable == false.
func (HealthCheck) Fix(context.Context) (doctor.FixResult, error) {
	return doctor.FixResult{}, doctor.ErrCheckNotFixable
}

// Run lists every device record and derives its Liveness. An unreadable
// record store is StatusError (Art.1: an unverifiable subject is never
// silently OK). An empty fleet is StatusOK with an explanatory message.
// Any node whose Liveness is not LivenessReachable downgrades the overall
// result to StatusWarn; the per-node detail lists exactly which nodes and
// their state.
func (c HealthCheck) Run(ctx context.Context) (doctor.CheckResult, error) {
	if err := ctx.Err(); err != nil {
		return doctor.CheckResult{Status: doctor.StatusError, Message: "nodes: check canceled"}, nil
	}
	if c.records == nil {
		return doctor.CheckResult{Status: doctor.StatusError,
			Message: "nodes: no device record store is configured on this installation"}, nil
	}
	recs, err := c.records.List()
	if err != nil {
		return doctor.CheckResult{Status: doctor.StatusError,
			Message: "nodes: could not read the device record store", Detail: err.Error()}, nil
	}
	if len(recs) == 0 {
		return doctor.CheckResult{Status: doctor.StatusOK, Message: "nodes: no nodes enrolled"}, nil
	}

	now := c.clock.Now()
	var notReachable, restrictedEligible []string
	for _, rec := range recs {
		if ComputeLiveness(rec, now, c.timeout) != LivenessReachable {
			notReachable = append(notReachable, rec.NodeID)
		}
		// Satisfies(GateRestricted) is reported for operator visibility
		// only — trust.go's own gate, never re-derived from anything this
		// check itself computes. It is never used to widen a decision:
		// this doctor check reports liveness/eligibility, it does not
		// grant anything.
		if Satisfies(rec.Tier, GateRestricted) {
			restrictedEligible = append(restrictedEligible, rec.NodeID)
		}
	}
	detail := fmt.Sprintf("restricted-gate eligible: %d of %d", len(restrictedEligible), len(recs))
	if len(notReachable) == 0 {
		return doctor.CheckResult{Status: doctor.StatusOK,
			Message: fmt.Sprintf("nodes: all %d enrolled node(s) reachable", len(recs)), Detail: detail}, nil
	}
	return doctor.CheckResult{
		Status:      doctor.StatusWarn,
		Message:     fmt.Sprintf("nodes: %d of %d enrolled node(s) not reachable", len(notReachable), len(recs)),
		Detail:      "not reachable: " + strings.Join(notReachable, ", ") + "; " + detail,
		Remediation: "check the node's connectivity and its `cascade node serve` process; a node not seen within the heartbeat timeout reports as not-reachable, never as a false OK",
	}, nil
}
