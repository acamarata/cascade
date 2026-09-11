package provider

// Purpose: the pure DTOs AD/S-61.T1 adds to pkg/provider for the
//
//	AgentProvider job-dispatch contract (R-16.68a): job identity and spec,
//	the R-16.37/R-21.140 run-state enum, the R-21.143 data-classification
//	lattice, worktree minimization, driver defaults, and the R-16.10
//	capacity-snapshot shape for agent lanes.
//
// Inputs: none at this layer — these are data shapes, not behavior, except
//
//	for JoinDataClass/WithDataClass, which are pure functions over the
//	DataClass lattice.
//
// Outputs: none beyond the functions above.
// Constraints: pkg/provider imports nothing from internal/ (Art.10.2); no
//
//	permissive zero value on AgentRunState or DataClass — an unresolved
//	DataClass reads as DataClassRestricted (fail closed), never as
//	unrestricted. CompliancePosture and its CredentialSharingForbidden
//	constant already exist in compliance.go (R-16.10, published by
//	P1-E10-W3-S19-T1) and are reused here rather than redeclared — see
//	this ticket's journal for the contract-vs-tree note.
//
// SPORT: pkg.provider.AgentProvider/EXTEND (P1-E30-W6-S61-T1).

import "time"

// AgentJobID identifies one spawned agent job.
type AgentJobID string

// WorktreeScope names the R-21.177 worktree-minimization set a spawned
// job's leased tree is materialized from: only these repo-relative
// prefixes plus the declared dependency paths, never the whole tree.
type WorktreeScope struct {
	// InScopePrefixes lists the repo-relative path prefixes the leased
	// worktree materializes.
	InScopePrefixes []string
	// DeclaredDeps lists additional repo-relative paths (typically
	// dependency manifests or shared packages) materialized alongside
	// InScopePrefixes even though they fall outside it.
	DeclaredDeps []string
}

// AgentJobSpec is the pure pkg DTO describing one job a caller asks an
// AgentProvider to Spawn. It carries no internal/jobs type.
type AgentJobSpec struct {
	// Prompt is the initial turn handed to the spawned agent.
	Prompt string
	// Scope is the R-21.177 worktree-minimization set the leased tree is
	// built from.
	Scope WorktreeScope
	// DataClass is the sensitivity tier this job's inputs carry. Required:
	// the zero value is not a member of the DataClass lattice and resolves
	// to DataClassRestricted via Resolved().
	DataClass DataClass
}

// WithDataClass returns a copy of s with DataClass raised to the more
// restrictive of s.DataClass and c (never lowered). A call that would
// lower the resolved class returns ErrDataClassDowngrade and the zero
// AgentJobSpec.
func (s AgentJobSpec) WithDataClass(c DataClass) (AgentJobSpec, error) {
	if c.Resolved().rank() < s.DataClass.Resolved().rank() {
		return AgentJobSpec{}, ErrDataClassDowngrade
	}
	s.DataClass = JoinDataClass(s.DataClass, c)
	return s, nil
}

// SpawnResult is Spawn's success return (R-21.177): the assigned job id
// and the process group the driver spawned the child into. ProcessGroupID
// is 0 only for an in-process lane that spawns no child process.
type SpawnResult struct {
	JobID          AgentJobID
	ProcessGroupID int
}

// AgentRunState is the pure pkg mirror of the R-16.37 job-lifecycle enum as
// amended by R-21.140. The zero value is deliberately not a member —
// internal/jobs maps this DTO to jobs.JobState at its own boundary, never
// the reverse.
type AgentRunState string

// The ten closed AgentRunState members, in R-16.37/R-21.140 order.
const (
	AgentRunPending    AgentRunState = "pending"
	AgentRunLeased     AgentRunState = "leased"
	AgentRunRunning    AgentRunState = "running"
	AgentRunVerifying  AgentRunState = "verifying"
	AgentRunReviewing  AgentRunState = "reviewing"
	AgentRunAccepted   AgentRunState = "accepted"
	AgentRunRejected   AgentRunState = "rejected"
	AgentRunCancelling AgentRunState = "cancelling"
	AgentRunCancelled  AgentRunState = "cancelled"
	AgentRunFailed     AgentRunState = "failed"
)

// Valid reports whether s is one of the ten closed AgentRunState members.
// The zero value ("") is not a member and reports false.
func (s AgentRunState) Valid() bool {
	switch s {
	case AgentRunPending, AgentRunLeased, AgentRunRunning, AgentRunVerifying,
		AgentRunReviewing, AgentRunAccepted, AgentRunRejected,
		AgentRunCancelling, AgentRunCancelled, AgentRunFailed:
		return true
	}
	return false
}

// terminalAgentRunStates holds the four AgentRunState members no further
// transition leaves. A lookup table, rather than a switch, keeps this
// independent of the exhaustive linter's enum-switch requirement — Valid
// above is the function that must list every member.
var terminalAgentRunStates = map[AgentRunState]bool{
	AgentRunAccepted:  true,
	AgentRunRejected:  true,
	AgentRunCancelled: true,
	AgentRunFailed:    true,
}

// Terminal reports whether s is one of the three states no further
// transition leaves (accepted, rejected, cancelled) or failed. Cancelling
// is explicitly NOT terminal: it is the sole gateway into cancelled, held
// until process-group exit is confirmed (R-21.140/R-21.174).
func (s AgentRunState) Terminal() bool {
	return terminalAgentRunStates[s]
}

// DataClass is the pure pkg enum mirroring the 06-FORGE-SPEC.md §5.16
// sensitivity tiers (R-21.143), ordered most to least restrictive:
// local-only > restricted > internal > public. The zero value is not a
// member; Resolved treats it (and any other unknown value) as
// DataClassRestricted — fail closed, never permissive.
type DataClass string

// The four closed DataClass members, plus dataClassRank's ordering.
const (
	DataClassPublic     DataClass = "public"
	DataClassInternal   DataClass = "internal"
	DataClassRestricted DataClass = "restricted"
	DataClassLocalOnly  DataClass = "local-only"
)

// dataClassRank orders the four members from least (0) to most (3)
// restrictive; Resolved's fallback and JoinDataClass both read through it.
var dataClassRank = map[DataClass]int{
	DataClassPublic:     0,
	DataClassInternal:   1,
	DataClassRestricted: 2,
	DataClassLocalOnly:  3,
}

// Valid reports whether d is one of the four closed DataClass members.
func (d DataClass) Valid() bool {
	_, ok := dataClassRank[d]
	return ok
}

// Resolved returns d if it is a valid member, else DataClassRestricted —
// unset, unknown or unresolvable data class reads as restricted, never as
// unrestricted (R-21.143 fail-closed rule).
func (d DataClass) Resolved() DataClass {
	if d.Valid() {
		return d
	}
	return DataClassRestricted
}

// rank returns d's resolved position in the restrictiveness ordering.
func (d DataClass) rank() int {
	return dataClassRank[d.Resolved()]
}

// JoinDataClass returns the more restrictive of a and b (after resolving
// each), the monotonic join every transformation in the agent-driver path
// applies when combining a spec's declared class with an observed one.
func JoinDataClass(a, b DataClass) DataClass {
	ar, br := a.Resolved(), b.Resolved()
	if ar.rank() >= br.rank() {
		return ar
	}
	return br
}

// DriverDefaults is the typed option struct the composition root injects
// into a driver: cancel-ladder timing, approval-wait timing, and status
// poll cadence. Ticket-local and testable; 08 §3 gains no row for it.
type DriverDefaults struct {
	// CancelGrace is how long Cancel waits after SIGTERM to the process
	// group before escalating to SIGKILL (R-21.174). Default 30s.
	CancelGrace time.Duration
	// ApprovalTimeout is how long an unresolved ApprovalRequest may sit
	// before the caller treats it as stale (R-21.171). Default 30m.
	ApprovalTimeout time.Duration
	// PollInterval is the cadence Status polling uses. Default 5s.
	PollInterval time.Duration
}

// NewDriverDefaults returns the R-21.174/R-21.171 defaults: 30s cancel
// grace, 30m approval timeout, 5s poll interval.
func NewDriverDefaults() DriverDefaults {
	return DriverDefaults{
		CancelGrace:     30 * time.Second,
		ApprovalTimeout: 30 * time.Minute,
		PollInterval:    5 * time.Second,
	}
}

// BucketState mirrors one J/S-20.T2 capacity bucket's shape for an agent
// lane: how much of the bucket has been used against its limit. A zero
// Limit means the bucket carries no enforced ceiling.
type BucketState struct {
	Used  float64
	Limit float64
}

// CapacityState is the pure pkg mirror of an agent lane's overall
// availability. The zero value is CapacityStateUnknown, not
// CapacityStateAvailable — an unresolved snapshot never silently reads as
// available.
type CapacityState string

// The closed CapacityState members. CapacityStateUnknown is the
// legitimate zero value.
//
// SDK-INTENT: CapacityStateAvailable/Constrained/Exhausted/AuthRequired are
// a contract only. No production caller sets them yet — CapacitySnapshot
// is consumed by AD/S-62.T2 (internal/conductor's agent-lane quota
// bridge), a later ticket than this one; they are declared here now so
// that ticket's driver-facing shape is already frozen.
const (
	CapacityStateUnknown      CapacityState = ""
	CapacityStateAvailable    CapacityState = "available"
	CapacityStateConstrained  CapacityState = "constrained"
	CapacityStateExhausted    CapacityState = "exhausted"
	CapacityStateAuthRequired CapacityState = "auth-required"
)

// CapacitySnapshot mirrors the J/S-20.T2 bucket shape for agent providers:
// three named buckets, an overall state, and an optional reset estimate.
type CapacitySnapshot struct {
	InteractiveUsage BucketState
	AgentSDKCredit   BucketState
	APICredit        BucketState
	State            CapacityState
	ResetEstimate    *time.Time
}

// OutputFilter is the pkg-typed custody seam every driver accepts: the
// AD/S-62.T3 host shim binds it to the H/S-16.T1 substitution pass so a
// driver's stdout/stderr is scanned and redacted before it ever reaches a
// caller.
//
// SDK-INTENT: this is a contract only. No production caller binds an
// OutputFilter yet — that wiring belongs to AD/S-62.T3's host shim, a
// later ticket than this one.
type OutputFilter func([]byte) ([]byte, error)

// ApprovalRequest is the pure DTO a driver surfaces via
// AgentProvider.ApprovalRequests / ResolveApproval. internal/conductor
// (AD/S-62.T2) bridges it to the I/S-18.T3 approval queue; no driver
// imports internal.
type ApprovalRequest struct {
	// ID identifies this request; ResolveApproval(id, token) resolves it.
	ID string
	// JobID names the job this approval blocks.
	JobID AgentJobID
	// Prompt is the human-readable text describing what is being
	// approved.
	Prompt string
	// DataClass is the joined sensitivity class of the material the
	// approval decision would expose (R-21.143). Required.
	DataClass DataClass
}

// CollectResult is Collect's success return: the job's final output,
// discovered artifact paths, and the joined data class of everything it
// carries.
type CollectResult struct {
	JobID     AgentJobID
	Output    string
	Artifacts []string
	// DataClass is the joined sensitivity class of Output and Artifacts
	// (R-21.143). Required.
	DataClass DataClass
}

// EgressClassAgentDriver names the egress class the AD/S-62.T3 host shim
// registers exactly once (R-16.68b, R-16.56 convention). This constant is
// the class NAME only — registration happens in the registrant's own
// package init, never here.
const EgressClassAgentDriver = "agent-driver"
