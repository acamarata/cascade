package provider

// Purpose: the pure DTOs AD/S-61.T1 adds to pkg/provider for the
// AgentProvider job-dispatch contract (R-16.68a): job identity and spec
// (with the R-14.329 spawn seams), the R-16.37/R-21.140 run-state enum, the
// R-21.143 data-class lattice, worktree minimization, driver defaults,
// spawn environments, the closed tool-policy enum and collect results
// (the R-16.10 capacity snapshot lives in agent_capacity.go).
// Inputs: none; data shapes plus the pure JoinSensitivity/WithDataClass and
// ResolveToolPolicy functions.
// Outputs: none beyond those functions.
// Constraints: imports nothing from internal/ (Art.10.2); no permissive
// zero value on AgentRunState or DataClass (the DataClass zero is
// Restricted); CompliancePosture is reused from compliance.go.
// SPORT: pkg.provider.AgentProvider/EXTEND (P1-E30-W6-S61-T1, S61-T2).

import (
	"time"

	"github.com/google/uuid"
)

// AgentJobID identifies one spawned agent job.
type AgentJobID string

// WorktreeScope is the R-21.177 minimization set a leased tree holds.
type WorktreeScope struct {
	// InScopePrefixes lists the repo-relative prefixes the tree holds.
	InScopePrefixes []string
	// DeclaredDeps lists extra repo-relative paths (dependency manifests,
	// shared packages) materialized alongside InScopePrefixes.
	DeclaredDeps []string
}

// SpawnEnv is the host-built environment for one spawn (R-21.151): Driver
// is the trusted child's exact environ (DriverEnvAllowlist), Tool the
// untrusted tool-subprocess environ (ToolEnvAllowlist, never the
// vendorAuthVar), Home the per-profile runtime home. AD/S-62.T3 fills it.
type SpawnEnv struct {
	Driver []string
	Tool   []string
	Home   string
}

// The closed AgentJobSpec.ToolPolicy enum (R-14.291).
const (
	ToolPolicyNone     = "none"
	ToolPolicyReadonly = "readonly"
	ToolPolicyAll      = "all"
)

// ResolveToolPolicy maps the zero value to ToolPolicyNone (fail closed)
// and refuses anything outside the closed enum with ErrInvalidToolPolicy.
func ResolveToolPolicy(p string) (string, error) {
	switch p {
	case "":
		return ToolPolicyNone, nil
	case ToolPolicyNone, ToolPolicyReadonly, ToolPolicyAll:
		return p, nil
	}
	return "", ErrInvalidToolPolicy
}

// AgentJobSpec describes one job a caller asks an AgentProvider to Spawn.
type AgentJobSpec struct {
	Prompt string // the initial turn handed to the spawned agent
	// Scope is the R-21.177 minimization set the leased tree is built from.
	Scope WorktreeScope
	// DataClass is the sensitivity tier this job's inputs carry; the zero
	// value is Restricted.
	DataClass DataClass
	// Env is the host-built spawn environment; an empty Env.Driver refuses.
	Env SpawnEnv
	// ToolPolicy is the closed none|readonly|all enum; "" resolves to none.
	ToolPolicy string
	// TicketID names the ticket the job serves; drivers derive the session
	// name from it (R-14.230), else from the JobID.
	TicketID string
	// WorktreeRoot is the absolute root of the minimized tree the job runs
	// in (the external driver's cmd.Dir); an external driver refuses "".
	WorktreeRoot string
	// Filter is the per-spawn output filter (R-14.329); an external driver
	// refuses a nil Filter with ErrEgressSubstitutionFailed.
	Filter OutputFilter
	// JobID, when set, is the job id Spawn uses and returns; "" lets the
	// driver mint one. An id already live in the driver is KindConflict.
	JobID AgentJobID
}

// WithDataClass returns s with DataClass raised to the join with c; a call
// that would lower the resolved class returns ErrDataClassDowngrade.
func (s AgentJobSpec) WithDataClass(c DataClass) (AgentJobSpec, error) {
	if s.DataClass.MoreRestrictiveThan(c) {
		return AgentJobSpec{}, ErrDataClassDowngrade
	}
	s.DataClass = JoinSensitivity(s.DataClass, c)
	return s, nil
}

// SpawnResult is Spawn's success return (R-21.177): the job id and the
// child's process group (0 only for an in-process lane).
type SpawnResult struct {
	JobID          AgentJobID
	ProcessGroupID int
}

// AgentRunState mirrors the R-16.37 job-lifecycle enum (R-21.140). The
// zero value is not a member; internal/jobs maps it at its own boundary.
type AgentRunState string

// The ten closed AgentRunState members (R-16.37/R-21.140 order).
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

// Valid reports whether s is one of the ten closed members ("" is not).
func (s AgentRunState) Valid() bool {
	switch s {
	case AgentRunPending, AgentRunLeased, AgentRunRunning, AgentRunVerifying,
		AgentRunReviewing, AgentRunAccepted, AgentRunRejected,
		AgentRunCancelling, AgentRunCancelled, AgentRunFailed:
		return true
	}
	return false
}

// terminalAgentRunStates holds the four members no transition leaves (a
// table, so only Valid must list every member for the exhaustive linter).
var terminalAgentRunStates = map[AgentRunState]bool{
	AgentRunAccepted:  true,
	AgentRunRejected:  true,
	AgentRunCancelled: true,
	AgentRunFailed:    true,
}

// Terminal reports accepted, rejected, cancelled or failed. Cancelling is
// not terminal: it holds until group exit is confirmed (R-21.174).
func (s AgentRunState) Terminal() bool {
	return terminalAgentRunStates[s]
}

// DataClass is the canonical sensitivity tier. Its zero is Restricted.
type DataClass = SensitivityTier

// Compatibility names for agent callers.
const (
	DataClassPublic     = SensitivityPublic
	DataClassInternal   = SensitivityInternal
	DataClassRestricted = SensitivityRestricted
	DataClassLocalOnly  = SensitivityLocalOnly
)

// DriverDefaults is the typed option struct the composition root injects
// into a driver: cancel-ladder timing, approval-wait timing, status poll
// cadence, and the optional spawn-flag values (R-14.288), each emitted
// only when non-empty/non-zero. 08 §3 gains no row for it.
type DriverDefaults struct {
	SessionNameFlag          string
	KernelPromptFile         string
	SubagentKernelPromptFile string
	PluginDir                string
	Effort                   string
	FallbackModel            string
	MCPConfigPath            string
	MaxTurns                 int
	MaxBudgetUSD             float64
	// CancelGrace is the SIGTERM-to-SIGKILL wait (R-21.174). Default 30s.
	CancelGrace time.Duration
	// ApprovalTimeout is when an unresolved approval is stale (R-21.171).
	ApprovalTimeout time.Duration
	// PollInterval is the cadence Status polling uses. Default 5s.
	PollInterval time.Duration
}

// NewDriverDefaults returns 30s cancel grace, 30m approval timeout and a
// 5s poll interval; every spawn-flag field stays empty/zero.
func NewDriverDefaults() DriverDefaults {
	return DriverDefaults{
		CancelGrace:     30 * time.Second,
		ApprovalTimeout: 30 * time.Minute,
		PollInterval:    5 * time.Second,
	}
}

// OutputFilter is the custody seam every driver accepts: the AD/S-62.T3
// host shim binds it to the H/S-16.T1 substitution pass so driver stdout
// and stderr are redacted before any caller sees them.
//
// SDK-INTENT: AD/S-62.T3's host shim is the first production binder.
type OutputFilter func([]byte) ([]byte, error)

// ExecutableReporter is implemented by every external agent driver: the
// absolute, symlink-resolved vendor CLI path it resolved at construction
// (the host shim's executable allowlist, R-14.329).
type ExecutableReporter interface {
	Executable() string
}

// ApprovalRequest is the DTO a driver surfaces via ApprovalRequests;
// internal/conductor (AD/S-62.T2) bridges it to the I/S-18.T3 queue.
type ApprovalRequest struct {
	// ID identifies this request; ResolveApproval(id, token) resolves it.
	ID string
	// JobID names the job this approval blocks.
	JobID AgentJobID
	// Prompt is the human-readable text describing what is approved.
	Prompt string
	// DataClass is the joined class of what the decision exposes.
	DataClass DataClass
}

// CollectResult is Collect's success return: the job's final output,
// discovered artifact paths, the joined data class of everything it
// carries, and the harness result fields a driver parsed (R-14.288).
type CollectResult struct {
	JobID     AgentJobID
	Output    string
	Artifacts []string
	// DataClass is the joined class of Output and Artifacts (R-21.143).
	DataClass DataClass
	// SessionID is the harness-reported session id, when one exists.
	SessionID string
	// StructuredOutput is the raw JSON a schema-constrained run returned.
	StructuredOutput []byte
	// Cost is a client-side observation when the harness reports one.
	Cost *CostEstimate
}

// CostSourceCLIObservation marks a cost read from the harness CLI's own
// result object: a client-side estimate, never billing truth (R-14.288).
const CostSourceCLIObservation = "cli-observation"

// CostEstimate is one cost observation and where it came from.
type CostEstimate struct {
	USD    float64
	Source string
}

// EgressClassAgentDriver names the egress class the AD/S-62.T3 host shim
// registers exactly once (R-16.68b); registration happens there.
const EgressClassAgentDriver = "agent-driver"

// SessionIDFor returns the harness session id for a job: the RFC 9562
// version-5 UUID of the job id under NameSpace_URL (a name-based
// identifier, not a security hash). The claude driver's --session-id and
// the principal session id both come from this one function.
func SessionIDFor(job AgentJobID) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(job)).String()
}
