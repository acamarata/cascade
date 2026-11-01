package jobs

// Purpose: JobSpecHash, the one digest of a job's acceptance-relevant spec
// (contract:ci-attestation, R18d MA-2). The controller binds it into the
// attestation nonce at Issue and the gate recomputes it; both call only
// this function.
//
// Inputs: a Job and the ids read from jobs_task_dependency for it.
// Outputs: hex SHA-256 over the canonical JSON of exactly id, capabilities,
// mutable_scope, risk_class, min_task_class, node_requirements,
// timeout_seconds, cost_ceiling, priority, consequence_class, data_class
// and the sorted depends_on ids.
// Constraints: state, created_at, updated_at and
// consecutive_failed_attempts never change the digest.
//
// SPORT: internal.jobs.JobSpecHash/ADDED (P1-CI-04).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"github.com/acamarata/cascade/pkg/provider"
)

// jobSpec is the hashed projection; field order is the canonical key order.
type jobSpec struct {
	ID               string   `json:"id"`
	Capabilities     []string `json:"capabilities"`
	MutableScope     string   `json:"mutable_scope"`
	RiskClass        string   `json:"risk_class"`
	MinTaskClass     string   `json:"min_task_class"`
	NodeRequirements string   `json:"node_requirements"`
	TimeoutSeconds   int64    `json:"timeout_seconds"`
	CostCeiling      float64  `json:"cost_ceiling"`
	Priority         int      `json:"priority"`
	ConsequenceClass string   `json:"consequence_class"`
	DataClass        string   `json:"data_class"`
	DependsOn        []string `json:"depends_on"`
}

// JobSpecHash returns the hex SHA-256 of j's spec plus its dependency ids,
// or "" for a spec with no canonical form (a NaN or infinite cost_ceiling,
// an unknown data_class); Issue refuses an empty hash, so such a job can
// never be attested.
func JobSpecHash(j Job, dependsOn []string) string {
	spec := jobSpec{
		ID: j.ID, Capabilities: append([]string{}, j.Capabilities...), MutableScope: j.MutableScope,
		RiskClass: j.RiskClass, MinTaskClass: j.MinTaskClass, NodeRequirements: j.NodeRequirements,
		TimeoutSeconds: j.TimeoutSeconds, CostCeiling: j.CostCeiling, Priority: j.Priority,
		ConsequenceClass: string(j.ConsequenceClass), DependsOn: slices.Sorted(slices.Values(dependsOn)),
	}
	if spec.DependsOn == nil {
		spec.DependsOn = []string{}
	}
	if j.DataClass != nil {
		name, err := provider.DataClassName(*j.DataClass)
		if err != nil {
			return ""
		}
		spec.DataClass = name
	}
	b, err := json.Marshal(spec)
	if err != nil { // only a NaN or infinite cost_ceiling fails to encode
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
