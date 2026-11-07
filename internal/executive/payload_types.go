package executive

// The closed v1 payload bodies of the R-21.37 exec.* kinds. Every field is
// required: a string must be non-empty, an int64 must be at least 1 and a
// *bool must be present (false is a valid value). No body carries a
// payload_version field; the envelope owns the version.

// ProjectPayload is the v1 body of the project group kinds and of
// exec.FINAL_ACCEPTANCE_READY.
type ProjectPayload struct {
	// ProjectID names the project the event concerns.
	ProjectID string `json:"project_id"`
}

// TaskPayload is the v1 body of the task group kinds except exec.TASK_FAILED.
type TaskPayload struct {
	// TaskID names the task the event concerns.
	TaskID string `json:"task_id"`
}

// TaskFailedPayload is the v1 body of exec.TASK_FAILED.
type TaskFailedPayload struct {
	// TaskID names the task that failed.
	TaskID string `json:"task_id"`
	// ConsecutiveFailedAttempts counts the failed attempts in a row, at least 1.
	ConsecutiveFailedAttempts int64 `json:"consecutive_failed_attempts"`
}

// AgentPayload is the v1 body of the agent group kinds.
type AgentPayload struct {
	// LaneID names the agent lane the event concerns.
	LaneID string `json:"lane_id"`
}

// ArtifactPayload is the v1 body of the artifact group kinds.
type ArtifactPayload struct {
	// TaskID names the task that produced the artifact.
	TaskID string `json:"task_id"`
	// Ref locates the artifact, evidence or patch.
	Ref string `json:"ref"`
}

// DecisionPayload is the v1 body of the decision group kinds.
type DecisionPayload struct {
	// SubjectID names what the decision is about.
	SubjectID string `json:"subject_id"`
}

// VerifyPayload is the v1 body of the verify group kinds.
type VerifyPayload struct {
	// SubjectID names what is being verified.
	SubjectID string `json:"subject_id"`
}

// QuotaPayload is the v1 body of the quota group kinds except exec.QUOTA_LOW.
type QuotaPayload struct {
	// LaneID names the lane whose quota changed.
	LaneID string `json:"lane_id"`
}

// QuotaLowPayload is the v1 body of exec.QUOTA_LOW.
type QuotaLowPayload struct {
	// LaneID names the lane running low.
	LaneID string `json:"lane_id"`
	// AffectedRole names the role the low quota affects.
	AffectedRole string `json:"affected_role"`
}

// WorkspacePayload is the v1 body of the workspace group kinds.
type WorkspacePayload struct {
	// TaskID names the task the worktree or lease serves.
	TaskID string `json:"task_id"`
}

// StatePayload is the v1 body of the state group kinds.
type StatePayload struct {
	// RuntimeSessionID names the runtime session whose state changed.
	RuntimeSessionID string `json:"runtime_session_id"`
}

// ExecutivePayload is the v1 body of the executive group kinds.
type ExecutivePayload struct { //nolint:revive // the name is fixed by the executive payload contract (ExecutivePayload{RuntimeSessionID})
	// RuntimeSessionID names the executive runtime session.
	RuntimeSessionID string `json:"runtime_session_id"`
}

// SafetyPayload is the v1 body of the safety group kinds except
// exec.PRIVACY_POLICY_BLOCKED.
type SafetyPayload struct {
	// SubjectID names the action or request the safety event concerns.
	SubjectID string `json:"subject_id"`
}

// PrivacyBlockedPayload is the v1 body of exec.PRIVACY_POLICY_BLOCKED.
type PrivacyBlockedPayload struct {
	// SubjectID names the blocked action or request.
	SubjectID string `json:"subject_id"`
	// SafeAlternativeExists reports whether a policy-safe route exists; it
	// must be present, and false is a valid value.
	SafeAlternativeExists *bool `json:"safe_alternative_exists"`
}
