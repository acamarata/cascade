package provider

// Purpose: the R-16.10 capacity snapshot an agent lane reports (the
//
//	J/S-20.T2 buckets and the closed CapacityState), split from
//	agent_types.go for the 300-line cap when AD/S-61.T2 added the spawn
//	seams there.
//
// Inputs: none; data shapes only.
// Outputs: none.
// Constraints: the zero CapacityState is unknown, never available.
// SPORT: pkg.provider.AgentProvider/EXTEND (P1-E30-W6-S61-T1, S61-T2).

import "time"

// BucketState mirrors one J/S-20.T2 capacity bucket (a zero Limit means
// no enforced ceiling).
type BucketState struct {
	Used  float64
	Limit float64
}

// CapacityState mirrors an agent lane's overall availability; the zero
// value is CapacityStateUnknown, never available.
type CapacityState string

// The closed CapacityState members.
//
// SDK-INTENT: a contract only; AD/S-62.T2 (internal/conductor's agent-lane
// quota bridge) is the first production setter.
const (
	CapacityStateUnknown      CapacityState = ""
	CapacityStateAvailable    CapacityState = "available"
	CapacityStateConstrained  CapacityState = "constrained"
	CapacityStateExhausted    CapacityState = "exhausted"
	CapacityStateAuthRequired CapacityState = "auth-required"
)

// CapacitySnapshot mirrors the J/S-20.T2 buckets for an agent lane.
type CapacitySnapshot struct {
	InteractiveUsage BucketState
	AgentSDKCredit   BucketState
	APICredit        BucketState
	State            CapacityState
	ResetEstimate    *time.Time
}
