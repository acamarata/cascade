// Package executive defines the closed exec.* event kind vocabulary.
package executive

import (
	"errors"
	"fmt"
)

// Kind is a closed executive event kind.
type Kind string

// Payload holds the raw event payload bytes.
type Payload []byte

// PayloadVersion identifies the schema version of a payload.
type PayloadVersion int

// Producer identifies the component that produced an event.
type Producer string

// ProjectID identifies the project scope of an event.
type ProjectID string

// RunID identifies the run scope of an event.
type RunID string

// StreamID identifies the stream scope of an event.
type StreamID string

// StreamSeq is the server-allocated per-stream monotonic sequence number.
type StreamSeq uint64

// KindGroup names the R-21.37 group an exec.* kind belongs to.
type KindGroup string

// The R-21.37 kind groups, in declaration order.
const (
	// GroupProject is the project kind group.
	GroupProject KindGroup = "project"
	// GroupTask is the task kind group.
	GroupTask KindGroup = "task"
	// GroupAgent is the agent kind group.
	GroupAgent KindGroup = "agent"
	// GroupArtifact is the artifact kind group.
	GroupArtifact KindGroup = "artifact"
	// GroupDecision is the decision kind group.
	GroupDecision KindGroup = "decision"
	// GroupVerify is the verify kind group.
	GroupVerify KindGroup = "verify"
	// GroupQuota is the quota kind group.
	GroupQuota KindGroup = "quota"
	// GroupWorkspace is the workspace kind group.
	GroupWorkspace KindGroup = "workspace"
	// GroupState is the state kind group.
	GroupState KindGroup = "state"
	// GroupExecutive is the executive kind group.
	GroupExecutive KindGroup = "executive"
	// GroupSafety is the safety kind group.
	GroupSafety KindGroup = "safety"
	// GroupAcceptance is the acceptance kind group.
	GroupAcceptance KindGroup = "acceptance"
)

// The closed R-21.37 exec.* kinds; each value is "exec." plus the R-21.37 name.
const (
	// KindProjectStarted is exec.PROJECT_STARTED (project group).
	KindProjectStarted Kind = "exec.PROJECT_STARTED"
	// KindPhaseChanged is exec.PHASE_CHANGED (project group).
	KindPhaseChanged Kind = "exec.PHASE_CHANGED"
	// KindProjectCompleted is exec.PROJECT_COMPLETED (project group).
	KindProjectCompleted Kind = "exec.PROJECT_COMPLETED"
	// KindProjectCancelled is exec.PROJECT_CANCELLED (project group).
	KindProjectCancelled Kind = "exec.PROJECT_CANCELLED"
	// KindTaskCreated is exec.TASK_CREATED (task group).
	KindTaskCreated Kind = "exec.TASK_CREATED"
	// KindTaskReady is exec.TASK_READY (task group).
	KindTaskReady Kind = "exec.TASK_READY"
	// KindTaskStarted is exec.TASK_STARTED (task group).
	KindTaskStarted Kind = "exec.TASK_STARTED"
	// KindTaskCompleted is exec.TASK_COMPLETED (task group).
	KindTaskCompleted Kind = "exec.TASK_COMPLETED"
	// KindTaskFailed is exec.TASK_FAILED (task group).
	KindTaskFailed Kind = "exec.TASK_FAILED"
	// KindTaskBlocked is exec.TASK_BLOCKED (task group).
	KindTaskBlocked Kind = "exec.TASK_BLOCKED"
	// KindAgentReserved is exec.AGENT_RESERVED (agent group).
	KindAgentReserved Kind = "exec.AGENT_RESERVED"
	// KindAgentStarted is exec.AGENT_STARTED (agent group).
	KindAgentStarted Kind = "exec.AGENT_STARTED"
	// KindAgentHeartbeat is exec.AGENT_HEARTBEAT (agent group).
	KindAgentHeartbeat Kind = "exec.AGENT_HEARTBEAT"
	// KindAgentStopped is exec.AGENT_STOPPED (agent group).
	KindAgentStopped Kind = "exec.AGENT_STOPPED"
	// KindArtifactWritten is exec.ARTIFACT_WRITTEN (artifact group).
	KindArtifactWritten Kind = "exec.ARTIFACT_WRITTEN"
	// KindEvidenceAdded is exec.EVIDENCE_ADDED (artifact group).
	KindEvidenceAdded Kind = "exec.EVIDENCE_ADDED"
	// KindPatchReady is exec.PATCH_READY (artifact group).
	KindPatchReady Kind = "exec.PATCH_READY"
	// KindDecisionRequired is exec.DECISION_REQUIRED (decision group).
	KindDecisionRequired Kind = "exec.DECISION_REQUIRED"
	// KindConflictDetected is exec.CONFLICT_DETECTED (decision group).
	KindConflictDetected Kind = "exec.CONFLICT_DETECTED"
	// KindScopeChange is exec.SCOPE_CHANGE (decision group).
	KindScopeChange Kind = "exec.SCOPE_CHANGE"
	// KindVerifyRequested is exec.VERIFY_REQUESTED (verify group).
	KindVerifyRequested Kind = "exec.VERIFY_REQUESTED"
	// KindVerifyPassed is exec.VERIFY_PASSED (verify group).
	KindVerifyPassed Kind = "exec.VERIFY_PASSED"
	// KindVerifyFailed is exec.VERIFY_FAILED (verify group).
	KindVerifyFailed Kind = "exec.VERIFY_FAILED"
	// KindQuotaLow is exec.QUOTA_LOW (quota group).
	KindQuotaLow Kind = "exec.QUOTA_LOW"
	// KindQuotaExhausted is exec.QUOTA_EXHAUSTED (quota group).
	KindQuotaExhausted Kind = "exec.QUOTA_EXHAUSTED"
	// KindQuotaRecovered is exec.QUOTA_RECOVERED (quota group).
	KindQuotaRecovered Kind = "exec.QUOTA_RECOVERED"
	// KindRateLimited is exec.RATE_LIMITED (quota group).
	KindRateLimited Kind = "exec.RATE_LIMITED"
	// KindWorktreeAllocated is exec.WORKTREE_ALLOCATED (workspace group).
	KindWorktreeAllocated Kind = "exec.WORKTREE_ALLOCATED"
	// KindLeaseAcquired is exec.LEASE_ACQUIRED (workspace group).
	KindLeaseAcquired Kind = "exec.LEASE_ACQUIRED"
	// KindLeaseConflict is exec.LEASE_CONFLICT (workspace group).
	KindLeaseConflict Kind = "exec.LEASE_CONFLICT"
	// KindLeaseExpired is exec.LEASE_EXPIRED (workspace group).
	KindLeaseExpired Kind = "exec.LEASE_EXPIRED"
	// KindCheckpointCreated is exec.CHECKPOINT_CREATED (state group).
	KindCheckpointCreated Kind = "exec.CHECKPOINT_CREATED"
	// KindContextCompacting is exec.CONTEXT_COMPACTING (state group).
	KindContextCompacting Kind = "exec.CONTEXT_COMPACTING"
	// KindContextCompacted is exec.CONTEXT_COMPACTED (state group).
	KindContextCompacted Kind = "exec.CONTEXT_COMPACTED"
	// KindT0WakeRequested is exec.T0_WAKE_REQUESTED (executive group).
	KindT0WakeRequested Kind = "exec.T0_WAKE_REQUESTED"
	// KindT0Awake is exec.T0_AWAKE (executive group).
	KindT0Awake Kind = "exec.T0_AWAKE"
	// KindT0Sleeping is exec.T0_SLEEPING (executive group).
	KindT0Sleeping Kind = "exec.T0_SLEEPING"
	// KindHumanApprovalRequired is exec.HUMAN_APPROVAL_REQUIRED (safety group).
	KindHumanApprovalRequired Kind = "exec.HUMAN_APPROVAL_REQUIRED"
	// KindPrivacyPolicyBlocked is exec.PRIVACY_POLICY_BLOCKED (safety group).
	KindPrivacyPolicyBlocked Kind = "exec.PRIVACY_POLICY_BLOCKED"
	// KindPermissionDenied is exec.PERMISSION_DENIED (safety group).
	KindPermissionDenied Kind = "exec.PERMISSION_DENIED"
	// KindFinalAcceptanceReady is exec.FINAL_ACCEPTANCE_READY (acceptance group).
	KindFinalAcceptanceReady Kind = "exec.FINAL_ACCEPTANCE_READY"
)

// kindGroups lists every group and its kinds in R-21.37 order.
var kindGroups = []struct {
	group KindGroup
	kinds []Kind
}{
	{GroupProject, []Kind{KindProjectStarted, KindPhaseChanged, KindProjectCompleted, KindProjectCancelled}},
	{GroupTask, []Kind{KindTaskCreated, KindTaskReady, KindTaskStarted, KindTaskCompleted, KindTaskFailed, KindTaskBlocked}},
	{GroupAgent, []Kind{KindAgentReserved, KindAgentStarted, KindAgentHeartbeat, KindAgentStopped}},
	{GroupArtifact, []Kind{KindArtifactWritten, KindEvidenceAdded, KindPatchReady}},
	{GroupDecision, []Kind{KindDecisionRequired, KindConflictDetected, KindScopeChange}},
	{GroupVerify, []Kind{KindVerifyRequested, KindVerifyPassed, KindVerifyFailed}},
	{GroupQuota, []Kind{KindQuotaLow, KindQuotaExhausted, KindQuotaRecovered, KindRateLimited}},
	{GroupWorkspace, []Kind{KindWorktreeAllocated, KindLeaseAcquired, KindLeaseConflict, KindLeaseExpired}},
	{GroupState, []Kind{KindCheckpointCreated, KindContextCompacting, KindContextCompacted}},
	{GroupExecutive, []Kind{KindT0WakeRequested, KindT0Awake, KindT0Sleeping}},
	{GroupSafety, []Kind{KindHumanApprovalRequired, KindPrivacyPolicyBlocked, KindPermissionDenied}},
	{GroupAcceptance, []Kind{KindFinalAcceptanceReady}},
}

// AllKinds is the closed R-21.37 list of every valid exec.* kind, in group order.
var AllKinds = flattenKinds()

func flattenKinds() []Kind {
	var all []Kind
	for _, g := range kindGroups {
		all = append(all, g.kinds...)
	}
	return all
}

// ErrUnknownExecKind is returned when a kind string is unset or not in AllKinds.
var ErrUnknownExecKind = errors.New("unknown exec kind")

// ParseExecKind parses s into a Kind, returning ErrUnknownExecKind for unset or unknown values.
func ParseExecKind(s string) (Kind, error) {
	k := Kind(s)
	if k == "" {
		return "", fmt.Errorf("%w: empty", ErrUnknownExecKind)
	}
	for _, v := range AllKinds {
		if v == k {
			return k, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownExecKind, s)
}

// GroupOf returns the R-21.37 group of k, or ErrUnknownExecKind when k is not in AllKinds.
func GroupOf(k Kind) (KindGroup, error) {
	for _, g := range kindGroups {
		for _, v := range g.kinds {
			if v == k {
				return g.group, nil
			}
		}
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownExecKind, k)
}
