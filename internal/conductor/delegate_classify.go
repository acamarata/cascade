// Purpose: the contract:delegate-rpc need and its pure classifier:
//   DelegationNeed carries what a delegation asks for, and ClassifyDelegation
//   fills its defaults, refuses anything outside the closed sets and derives
//   the tool policy.
// Inputs: a DelegationNeed.
// Outputs: the defaulted DelegationNeed and its provider.ToolPolicy* value,
//   or ErrInvalidParam.
// Constraints: pure function only; no clock, config, store or network. A
//   refusal returns the sentinel itself, never a wrap of it.
// SPORT: conductor.delegate/ADD (P1-AGT-08a).

package conductor

import (
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// The closed quality and deadline literals, copied from
// internal/fleet/capacity/request.go (QualityEnum and DeadlineEnum), which
// this package may not import.
const (
	delegationQualityStandard     = "standard"
	delegationQualityHigh         = "high"
	delegationQualityMax          = "max"
	delegationDeadlineInteractive = "interactive"
	delegationDeadlineBatch       = "batch"
)

// ErrInvalidParam is returned, unwrapped, for any delegation parameter
// ClassifyDelegation refuses.
var ErrInvalidParam = cascade.New(cascade.KindInvalidInput, "conductor: invalid delegation parameter")

// DelegationNeed is what one delegation asks for. The RPC params task_class,
// min_quality, deadline, mutation and job_id map one to one onto its fields.
type DelegationNeed struct {
	// TaskClass is RPC param task_class, one of the closed nine taskClasses.
	TaskClass string
	// MinQuality is RPC param min_quality: standard, high or max.
	MinQuality string
	// Deadline is RPC param deadline: interactive or batch.
	Deadline string
	// Mutation is RPC param mutation: the delegation may change a worktree.
	Mutation bool
	// JobID is RPC param job_id, required when Mutation is true.
	JobID string
	// Sensitivity is set by the daemon, never from RPC params; zero means
	// restricted.
	Sensitivity provider.SensitivityTier
}

// ClassifyDelegation fills n's defaults and returns it with its tool policy:
// provider.ToolPolicyAll when n.Mutation, else provider.ToolPolicyReadonly.
// TaskClass defaults to code for a mutation and reason otherwise, MinQuality
// to standard and Deadline to interactive. A valid Sensitivity and JobID come
// back unchanged. A mutation without a job, or any value outside its closed
// set, returns the zero DelegationNeed, "" and ErrInvalidParam.
func ClassifyDelegation(n DelegationNeed) (DelegationNeed, string, error) {
	if n.Mutation && strings.TrimSpace(n.JobID) == "" {
		return DelegationNeed{}, "", ErrInvalidParam
	}
	if n.TaskClass == "" {
		n.TaskClass = string(TaskClassReason)
		if n.Mutation {
			n.TaskClass = string(TaskClassCode)
		}
	} else if !taskClasses[n.TaskClass] {
		return DelegationNeed{}, "", ErrInvalidParam
	}
	if n.MinQuality == "" {
		n.MinQuality = delegationQualityStandard
	} else if !validDelegationQuality(n.MinQuality) {
		return DelegationNeed{}, "", ErrInvalidParam
	}
	if n.Deadline == "" {
		n.Deadline = delegationDeadlineInteractive
	} else if !validDelegationDeadline(n.Deadline) {
		return DelegationNeed{}, "", ErrInvalidParam
	}
	if !n.Sensitivity.Valid() {
		return DelegationNeed{}, "", ErrInvalidParam
	}
	if n.Mutation {
		return n, provider.ToolPolicyAll, nil
	}
	return n, provider.ToolPolicyReadonly, nil
}

// validDelegationQuality reports whether q is in the closed quality set.
func validDelegationQuality(q string) bool {
	switch q {
	case delegationQualityStandard, delegationQualityHigh, delegationQualityMax:
		return true
	}
	return false
}

// validDelegationDeadline reports whether d is in the closed deadline set.
func validDelegationDeadline(d string) bool {
	switch d {
	case delegationDeadlineInteractive, delegationDeadlineBatch:
		return true
	}
	return false
}
